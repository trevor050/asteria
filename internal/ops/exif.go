package ops

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// JPEGOrientation returns the EXIF orientation tag (1-8) of a JPEG file, or 0
// when the file has none or cannot be parsed. It reads only the header.
func JPEGOrientation(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return 0
	}
	for {
		var marker [2]byte
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return 0
		}
		// Start of scan or end of image: no EXIF before the image data.
		if marker[1] == 0xDA || marker[1] == 0xD9 {
			return 0
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return 0
		}
		size := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if size < 0 {
			return 0
		}
		if marker[1] != 0xE1 {
			if _, err := r.Discard(size); err != nil {
				return 0
			}
			continue
		}
		seg := make([]byte, size)
		if _, err := io.ReadFull(r, seg); err != nil {
			return 0
		}
		if o := orientationFromExif(seg); o > 0 {
			return o
		}
	}
}

func orientationFromExif(seg []byte) int {
	if len(seg) < 14 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	tiff := seg[6:]
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd+2 > len(tiff) {
		return 0
	}
	n := int(bo.Uint16(tiff[ifd : ifd+2]))
	for i := 0; i < n; i++ {
		off := ifd + 2 + i*12
		if off+12 > len(tiff) {
			return 0
		}
		if bo.Uint16(tiff[off:off+2]) == 0x0112 {
			v := int(bo.Uint16(tiff[off+8 : off+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}
