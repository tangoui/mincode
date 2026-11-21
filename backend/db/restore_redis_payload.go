package db

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Redis DUMP contains one RDB value followed by a little-endian RDB version
// and Redis CRC64/Jones. Integrity does not by itself validate the body.
func validateRedisDump(data []byte) error {
	if len(data) < 11 {
		return fmt.Errorf("restore_invalid_redis_dump")
	}
	version := binary.LittleEndian.Uint16(data[len(data)-10:])
	if version < 1 || version > 15 {
		return fmt.Errorf("restore_redis_version_requires_manual_recovery")
	}
	var crc uint64
	for _, b := range data[:len(data)-8] {
		crc ^= uint64(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x95ac9329ac4bc9b5
			} else {
				crc >>= 1
			}
		}
	}
	if crc != binary.LittleEndian.Uint64(data[len(data)-8:]) {
		return fmt.Errorf("restore_invalid_redis_dump_checksum")
	}
	r := bytes.NewReader(data[:len(data)-10])
	kind, _ := r.ReadByte()
	switch kind {
	case 0:
		if err := readRDBString(r); err != nil {
			return err
		}
	case 1, 2, 4, 5:
		count, encoded, err := readRDBLength(r)
		if err != nil || encoded || count > uint64(r.Len()) {
			return fmt.Errorf("restore_invalid_redis_dump")
		}
		for i := uint64(0); i < count; i++ {
			if err := readRDBString(r); err != nil {
				return err
			}
			switch kind {
			case 4:
				if err := readRDBString(r); err != nil {
					return err
				}
			case 5:
				if r.Len() < 8 {
					return io.ErrUnexpectedEOF
				}
				r.Seek(8, io.SeekCurrent)
			}
		}
	default:
		return fmt.Errorf("restore_redis_encoding_requires_manual_recovery")
	}
	if r.Len() != 0 {
		return fmt.Errorf("restore_invalid_redis_dump_trailing")
	}
	return nil
}
func readRDBLength(r *bytes.Reader) (uint64, bool, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, false, err
	}
	switch b >> 6 {
	case 0:
		return uint64(b & 63), false, nil
	case 1:
		c, err := r.ReadByte()
		return uint64(b&63)<<8 | uint64(c), false, err
	case 3:
		return uint64(b & 63), true, nil
	case 2:
		var n int
		if b == 0x80 {
			n = 4
		} else if b == 0x81 {
			n = 8
		} else {
			return 0, false, fmt.Errorf("restore_invalid_redis_length")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, false, err
		}
		if n == 4 {
			return uint64(binary.BigEndian.Uint32(buf)), false, nil
		}
		return binary.BigEndian.Uint64(buf), false, nil
	}
	return 0, false, io.ErrUnexpectedEOF
}
func readRDBString(r *bytes.Reader) error {
	n, encoded, err := readRDBLength(r)
	if err != nil {
		return err
	}
	if encoded {
		switch n {
		case 0:
			n = 1
		case 1:
			n = 2
		case 2:
			n = 4
		default:
			return fmt.Errorf("restore_redis_compressed_value_requires_manual_recovery")
		}
	}
	if n > uint64(r.Len()) {
		return fmt.Errorf("restore_invalid_redis_string")
	}
	_, err = r.Seek(int64(n), io.SeekCurrent)
	return err
}
