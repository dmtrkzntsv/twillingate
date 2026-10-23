package archive

import (
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// pageCodec is the writer's zstd: parquet-go's level, with a window of
// zstdWindow and encoders kept between pages. parquet-go's own codec uses
// zstd's default 8 MB window, for which an encoder holds a 16 MB history,
// and keeps its encoders in a pool every GC empties, so a writer made a
// new one, history and all, after each: 288 MB allocated in one month's
// merge. A page is at most a few hundred KiB, so the smaller window loses
// nothing, and the pages decode as before.
type pageCodec struct{ encoders chan *zstd.Encoder }

// zstdWindow bounds what a page's compression looks back on: a data page
// is about pageBytes, a dictionary page up to dictMaxBytes; both fit.
const zstdWindow = 256 << 10

// zstdPages keeps up to 4 encoders, as many writers as compress at once
// (the pass writes one file at a time).
var zstdPages = &pageCodec{encoders: make(chan *zstd.Encoder, 4)}

func (c *pageCodec) String() string                            { return "ZSTD" }
func (c *pageCodec) CompressionCodec() format.CompressionCodec { return format.Zstd }

func (c *pageCodec) Encode(dst, src []byte) ([]byte, error) {
	var e *zstd.Encoder
	select {
	case e = <-c.encoders:
	default:
		var err error
		e, err = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithZeroFrames(true), zstd.WithEncoderCRC(false), zstd.WithWindowSize(zstdWindow), zstd.WithLowerEncoderMem(true))
		if err != nil {
			return nil, err
		}
	}
	out := e.EncodeAll(src, dst[:0])
	select {
	case c.encoders <- e:
	default:
	}
	return out, nil
}

// Decode is parquet-go's: the writer only encodes.
func (c *pageCodec) Decode(dst, src []byte) ([]byte, error) { return parquet.Zstd.Decode(dst, src) }
