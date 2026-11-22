/* Small WebAssembly entry points for the pinned zlib-ng compressor. */
#include <stdint.h>
#include <stdlib.h>
#include "zlib-ng.h"

static uint8_t *scratch;
static size_t input_size, output_capacity;

uintptr_t wire_reserve(uint32_t size) {
    if (size > 1048576) return 0;
    const size_t capacity = zng_compressBound(size);
    uint8_t *next = realloc(scratch, size + capacity);
    if (!next) return 0;
    scratch = next;
    input_size = size;
    output_capacity = capacity;
    return (uintptr_t)scratch;
}

/* ZLibStream.Write processes the input before Dispose finishes the stream. */
uint32_t wire_encode(void) {
    if (!scratch) return 0;
    zng_stream z = {0};
    if (zng_deflateInit2(&z, 6, Z_DEFLATED, 15, 8, Z_DEFAULT_STRATEGY) != Z_OK) return 0;
    z.next_in = scratch;
    z.avail_in = input_size;
    z.next_out = scratch + input_size;
    z.avail_out = output_capacity;
    int status = input_size ? zng_deflate(&z, Z_NO_FLUSH) : Z_OK;
    if (status == Z_OK && z.avail_in == 0) status = zng_deflate(&z, Z_FINISH);
    uint32_t size = status == Z_STREAM_END ? z.total_out : 0;
    zng_deflateEnd(&z);
    return size;
}
