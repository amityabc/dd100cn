package protocol

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"runtime"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// The accepted client's channel and marked game packets expose zlib-ng's
// complete byte stream. The same input can have different valid encodings.
// build_zlib.py pins and rebuilds the standalone compressor used by .NET 10.
//
//go:embed zlibng.wasm
var zlibModule []byte

type zlibInstance struct {
	module          api.Module
	reserve, encode api.Function
}

type compressor struct {
	runtime   wazero.Runtime
	code      wazero.CompiledModule
	available chan *zlibInstance
}

var loadCompressor = sync.OnceValues(func() (*compressor, error) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(128))
	code, err := rt.CompileModule(ctx, zlibModule)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	// The toolchain's allocator initializes its internal canary via this one
	// WASI import. The compressor has no other host imports.
	_, err = rt.NewHostModuleBuilder("wasi_snapshot_preview1").NewFunctionBuilder().WithFunc(
		func(_ context.Context, m api.Module, ptr, n uint32) uint32 {
			buf, ok := m.Memory().Read(ptr, n)
			if !ok {
				return 21
			} // WASI EFAULT
			rand.Read(buf)
			return 0
		}).Export("random_get").Instantiate(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	pool := make(chan *zlibInstance, min(8, runtime.GOMAXPROCS(0)))
	for range cap(pool) {
		pool <- nil
	}
	return &compressor{rt, code, pool}, nil
})

func (c *compressor) compress(src []byte) ([]byte, error) {
	ctx := context.Background()
	z := <-c.available
	defer func() { c.available <- z }()
	if z == nil {
		module, err := c.runtime.InstantiateModule(ctx, c.code, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
		if err != nil {
			return nil, err
		}
		z = &zlibInstance{module, module.ExportedFunction("wire_reserve"), module.ExportedFunction("wire_encode")}
	}
	result, err := z.reserve.Call(ctx, uint64(len(src)))
	if err != nil {
		z.module.Close(ctx)
		z = nil
		return nil, err
	}
	address := uint32(result[0])
	if address == 0 || !z.module.Memory().Write(address, src) {
		return nil, errors.New("zlib-ng input allocation failed")
	}
	result, err = z.encode.Call(ctx)
	if err != nil {
		z.module.Close(ctx)
		z = nil
		return nil, err
	}
	size := uint32(result[0])
	b, ok := z.module.Memory().Read(address+uint32(len(src)), size)
	if !ok || size == 0 {
		return nil, errors.New("zlib-ng returned an invalid output span")
	}
	return bytes.Clone(b), nil
}

func compressWire(src []byte) ([]byte, error) {
	if len(src) > MaximumFrameBytes {
		return nil, errors.New("compression input too large")
	}
	// .NET's ZLibStream writes no header or trailer until its first
	// nonempty write. An empty Write followed by Dispose yields zero bytes.
	if len(src) == 0 {
		return []byte{}, nil
	}
	c, err := loadCompressor()
	if err != nil {
		return nil, err
	}
	return c.compress(src)
}

// CompressPayload produces the original level-six zlib-ng stream for
// marker16 game notifications and protected channel messages.
func CompressPayload(data []byte) ([]byte, error) { return compressWire(data) }
