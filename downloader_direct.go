package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/canhlinh/log4go"
	"github.com/canhlinh/pluto" // đảm bảo trỏ đúng repo mới
	"gopkg.in/cheggaaa/pb.v1"
)

// Các hằng số cấu hình
const (
	DefaultMaxParts = 20
)

var (
	DefaultSlowDuration       = 30
	DefaultSlowSpeed    int64 = 100000 // 100KB/s
)

type DirectDownloader struct {
	*Base
	pluto *pluto.Pluto
}

func NewDirectDownloader(fileID string, source *DownloadSource) *DirectDownloader {
	d := &DirectDownloader{}
	d.Base = NewBase(fileID, source)
	return d
}

func (d *DirectDownloader) init() error {
	cookies := []*http.Cookie{}
	if len(d.DlSource.Cookies) > 0 {
		for _, cookie := range d.DlSource.Cookies {
			cookies = append(cookies, &http.Cookie{
				Name:  cookie.Name,
				Value: cookie.Value,
			})
		}
	}

	headers := []string{}
	for key, value := range d.DlSource.Header {
		headers = append(headers, fmt.Sprintf("%s:%s", key, value))
	}
	headers = append(headers, fmt.Sprintf("%s:%s", "Cookie", CookiesToHeader(cookies)))

	fileURL, err := url.Parse(d.DlSource.Value)
	if err != nil {
		return err
	}

	// Lấy số kết nối từ request, nếu không có thì dùng mặc định 20
	maxParts := d.DlSource.MaxParts
	if maxParts <= 0 {
		maxParts = DefaultMaxParts
	}

	log4go.Info("Khởi tạo download với %v kết nối cho file %s", maxParts, d.FileID)

	// Truyền số kết nối (maxParts) vào Pluto
	d.pluto, err = pluto.New(fileURL, headers, uint(maxParts), false, d.DlSource.Proxy)
	if err != nil {
		return err
	}
	return nil
}

func (d *DirectDownloader) Do() (result *DownloadResult, err error) {
	if err := d.init(); err != nil {
		return nil, err
	}

	quit := make(chan bool)
	dir := makeDownloadDir()
	
	// Tự động dọn dẹp nếu có lỗi xảy ra
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()

	f, err := os.CreateTemp(dir, d.Base.FileID)
	if err != nil {
		return nil, err
	}

	bar := pb.StartNew(0)
	bar.SetUnits(pb.U_BYTES)
	bar.ShowSpeed = true
	bar.ShowBar = false

	defer func() {
		bar.Finish()
		f.Close()
		close(quit)
	}()

	// Tạo context có thể cancel để ngắt Pluto khi cần
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Goroutine giám sát tốc độ
	go func() {
		period := time.Duration(DefaultSlowDuration)
		ticker := time.NewTicker(period * time.Second)
		defer ticker.Stop()
		
		var lastDownloaded int64

		for {
			select {
			case <-ticker.C:
				current := bar.Get()
				avgSpeed := (current - lastDownloaded) / int64(period)
				
				if avgSpeed < DefaultSlowSpeed {
					log4go.Warn("[%s] Tải quá chậm (%v Kbs). Kích hoạt ngắt luồng!", d.FileID, float64(avgSpeed)/1000)
					cancel() // Gửi lệnh dừng tới Pluto
					return
				}
				lastDownloaded = current
			case s, ok := <-d.pluto.StatsChan:
				if !ok { return }
				bar.Set64(int64(s.Downloaded))
			case <-d.pluto.Finished:
				return
			case <-ctx.Done():
				return
			case <-quit:
				return
			}
		}
	}()

	log4go.Info("Bắt đầu Pluto Download: %s", d.DlSource.Value)
	
	// Gọi hàm Download của Pluto (nhớ dùng bản Pluto đã sửa để nhận ctx)
	if r, dlErr := d.pluto.Download(ctx, f); dlErr != nil {
		if errors.Is(dlErr, context.Canceled) || strings.Contains(dlErr.Error(), "context canceled") {
			log4go.Error("[%s] Đã hủy tải do tốc độ không đạt yêu cầu", d.FileID)
			return nil, errors.New("cancelled due to slow download speed")
		}
		return nil, dlErr
	} else {
		log4go.Info("[%s] Tải xong! Kích thước: %v", d.FileID, r.Size)
	}

	result = &DownloadResult{
		FileID: d.FileID,
		Path:   f.Name(),
		Dir:    dir,
	}
	return result, nil
}
