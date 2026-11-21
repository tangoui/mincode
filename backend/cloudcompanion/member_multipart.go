package cloudcompanion

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const marketplaceUploadMaxBytes = 64 << 20

var errMarketplaceFileChanged = errors.New("安装包在确认后发生变化，已停止上传")

// Like marketArtifactClient, deliberately bypass environment proxies/cookies and never redirect
// member credentials. Uploads need a longer timeout than ordinary member requests.
var marketPublishClient = &http.Client{
	Timeout: 10 * time.Minute,
	Transport: &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func marketUploadFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("安装包必须是普通文件，不能是符号链接")
	}
	if info.Size() > marketplaceUploadMaxBytes {
		return nil, errors.New("安装包超过云端上限 64 MiB")
	}
	if info.Size() < 1 {
		return nil, errors.New("安装包不能为空")
	}
	return info, nil
}

func memberMultipartPost(ctx context.Context, path string, fields url.Values, fileField, filePath, fileName, expectedSHA256 string,
	progress func(sent, total int64), result any) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Reject oversized/special files before a possibly necessary token-refresh network call.
	if _, err := marketUploadFileInfo(filePath); err != nil {
		return err
	}
	session, err := publishSession(ctx)
	if err != nil {
		return err
	}
	return memberMultipartPostSession(ctx, session, path, fields, fileField, filePath, fileName, expectedSHA256, progress, result)
}

func memberMultipartPostSession(ctx context.Context, session persistedSession, path string, fields url.Values, fileField, filePath, fileName, expectedSHA256 string,
	progress func(sent, total int64), result any) (err error) {
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := marketUploadFileInfo(filePath); err != nil {
		return err
	}
	if len(expectedSHA256) != 64 {
		return errors.New("安装包 SHA-256 无效")
	}
	return withMemberToken(ctx, session, func(token string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !sameMemberSession(session) {
			return publishAccountChanged()
		}
		// Every retry takes a new lstat/open snapshot; never reuse an exhausted file handle.
		info, err := marketUploadFileInfo(filePath)
		if err != nil {
			return err
		}
		file, err := os.Open(filePath)
		if err != nil {
			return errors.New("无法读取安装包")
		}
		defer file.Close()
		opened, err := file.Stat()
		current, statErr := os.Lstat(filePath)
		if err != nil || statErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
			!os.SameFile(info, opened) || !os.SameFile(info, current) || info.Size() != opened.Size() {
			return errMarketplaceFileChanged
		}
		reader, writer := io.Pipe()
		body := multipart.NewWriter(writer)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(session.APIBaseURL, "/")+"/api/"+strings.TrimLeft(path, "/"), reader)
		if err != nil {
			reader.Close()
			writer.Close()
			return errors.New("无法创建安装包上传请求")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", body.FormDataContentType())
		req.Header.Set("ba-user-token", token)
		done := make(chan error, 1)
		go func() {
			err := writeMarketMultipart(ctx, body, fields, fileField, fileName, file, info, expectedSHA256, progress)
			// On a digest mismatch, NO final multipart boundary or terminal HTTP chunk is sent.
			// The cloud must observe a truncated body, never an apparently complete wrong package.
			_ = writer.CloseWithError(err)
			done <- err
		}()
		response, requestErr := marketPublishClient.Do(req)
		// A server may reject auth before consuming the file. Unblock and join the producer in
		// every case, including cancellation, so no goroutine/callback/file survives this call.
		_ = reader.Close()
		writeErr := <-done
		if response != nil {
			defer response.Body.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(writeErr, errMarketplaceFileChanged) {
			return writeErr
		}
		if requestErr != nil {
			return errors.New("安装包上传失败，请检查网络后重试")
		}
		if !sameMemberSession(session) {
			return publishAccountChanged()
		}
		err = decodeDeveloperResponse(response, result)
		if err != nil {
			return err
		}
		if writeErr != nil {
			return errors.New("读取或上传安装包失败，请重试")
		}
		return nil
	})
}

type marketProgressWriter struct {
	writer      io.Writer
	ctx         context.Context
	sent, total int64
	progress    func(int64, int64)
}

func (w *marketProgressWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(data)
	w.sent += int64(n)
	if w.progress != nil {
		w.progress(w.sent, w.total)
	}
	if w.ctx.Err() != nil {
		return n, w.ctx.Err()
	}
	return n, err
}

func writeMarketMultipart(ctx context.Context, body *multipart.Writer, fields url.Values, fileField, fileName string, file *os.File, info os.FileInfo, expected string, progress func(int64, int64)) error {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range fields[key] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := body.WriteField(key, value); err != nil {
				return err
			}
		}
	}
	part, err := body.CreateFormFile(fileField, fileName)
	if err != nil {
		return err
	}
	hash := sha256.New()
	out := &marketProgressWriter{writer: part, ctx: ctx, total: info.Size(), progress: progress}
	n, err := io.Copy(out, io.TeeReader(io.LimitReader(file, marketplaceUploadMaxBytes+1), hash))
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || n != info.Size() || after.Size() != info.Size() || !strings.EqualFold(fmt.Sprintf("%x", hash.Sum(nil)), expected) {
		return errMarketplaceFileChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return body.Close()
}
