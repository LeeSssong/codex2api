package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"
)

func TestBasispointsImagePreflightRejectsBeforeAnyStorage(t *testing.T) {
	good := dataURL(solidPNG(t, 4, color.RGBA{A: 255}))
	many := make([]string, 21)
	for i := range many {
		many[i] = good
	}
	for name, urls := range map[string][]string{
		"invalid second image": {good, "data:image/png;base64,bm90IGFuIGltYWdl"},
		"twenty one images":    many,
	} {
		t.Run(name, func(t *testing.T) {
			host := &fakeImageHost{}
			host.install(t)
			var parts []map[string]string
			for _, u := range urls {
				parts = append(parts, map[string]string{"type": "input_image", "image_url": u})
			}
			body, _ := json.Marshal(map[string]interface{}{"input": []interface{}{map[string]interface{}{"type": "message", "content": parts}}})
			_, _, err := rewriteBasispointsImages(context.Background(), body)
			if err == nil {
				t.Fatal("invalid complete request accepted")
			}
			if len(host.calls) != 0 {
				t.Fatalf("stored %d images before completing preflight", len(host.calls))
			}
		})
	}
}
func TestBasispointsImageRejectsFalseMIMEAndAllowsTwentyMiB(t *testing.T) {
	png := solidPNG(t, 4, color.RGBA{A: 255})
	if _, _, err := decodeInlineImage("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(png)); err == nil {
		t.Error("false MIME accepted")
	}
	padded := append(png, make([]byte, (20<<20)-len(png))...)
	if _, _, err := decodeInlineImage(dataURL(padded)); err != nil {
		t.Errorf("valid 20 MiB image rejected: %v", err)
	}
}

func TestBasispointsImageTotalBytesRejectedBeforeStorage(t *testing.T) {
	host := &fakeImageHost{}
	host.install(t)
	png := solidPNG(t, 4, color.RGBA{A: 255})
	a := append(append([]byte{}, png...), make([]byte, (16<<20)-len(png))...)
	b := append(append([]byte{}, a...), 0)
	parts := []map[string]string{{"type": "input_image", "image_url": dataURL(a)}, {"type": "input_image", "image_url": dataURL(b)}}
	body, _ := json.Marshal(map[string]interface{}{"input": []interface{}{map[string]interface{}{"content": parts}}})
	_, _, err := rewriteBasispointsImages(context.Background(), body)
	status, _, _ := basispointsImageErrorStatus(err)
	if status != 413 || len(host.calls) != 0 {
		t.Fatalf("32 MiB+1 must reject before storage: %v calls=%d", err, len(host.calls))
	}
}
func TestBasispointsImageCancelledBeforeStorage(t *testing.T) {
	host := &fakeImageHost{}
	host.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, _ := json.Marshal(map[string]interface{}{"input": []interface{}{map[string]interface{}{"content": []map[string]string{{"type": "input_image", "image_url": dataURL(solidPNG(t, 4, color.RGBA{A: 255}))}}}}})
	_, _, err := rewriteBasispointsImages(ctx, body)
	if err != context.Canceled || len(host.calls) != 0 {
		t.Fatalf("cancelled request stored an image: %v", err)
	}
}

func TestBasispointsImageRasterFormatsAndPixelBudget(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var jpegData, gifData bytes.Buffer
	if err := jpeg.Encode(&jpegData, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifData, img, nil); err != nil {
		t.Fatal(err)
	}
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for mime, data := range map[string][]byte{"image/png": solidPNG(t, 4, color.RGBA{A: 255}), "image/jpeg": jpegData.Bytes(), "image/gif": gifData.Bytes(), "image/webp": webp} {
		t.Run(mime, func(t *testing.T) {
			decoded, got, err := decodeInlineImage("data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data))
			if err != nil || got != mime || !bytes.Equal(decoded, data) {
				t.Fatalf("valid raster rejected: %s %v", got, err)
			}
		})
	}
	png := solidPNG(t, 4, color.RGBA{A: 255})
	binary.BigEndian.PutUint32(png[16:20], 100000)
	binary.BigEndian.PutUint32(png[20:24], 100000)
	binary.BigEndian.PutUint32(png[29:33], crc32.ChecksumIEEE(png[12:29]))
	if _, _, err := decodeInlineImage(dataURL(png)); err == nil {
		t.Fatal("100000x100000 image accepted")
	}
}
