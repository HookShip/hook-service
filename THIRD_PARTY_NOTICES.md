# Third-party notices

This file records third-party material incorporated into distributed
artifacts, plus external references that require an explicit provenance
boundary.

## Go

Compiled artifacts may contain portions of the Go runtime and standard
library.

Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of Google Inc. nor the names of its contributors may be
   used to endorse or promote products derived from this software without
   specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.

Source and license: <https://go.dev/LICENSE>

## pgx and selected Go module dependencies

The durability library uses these modules:

- `github.com/jackc/pgx/v5` v5.10.0 — MIT License;
- `github.com/jackc/pgpassfile` v1.0.0 — MIT License;
- `github.com/jackc/pgservicefile`
  v0.0.0-20240606120523-5a60cdf6a761 — MIT License;
- `github.com/jackc/puddle/v2` v2.2.2 — MIT License;
- `golang.org/x/sync` v0.17.0 — BSD 3-Clause License;
- `golang.org/x/text` v0.29.0 — BSD 3-Clause License.

## Runtime and profile dependencies

The delivery runtime and its selectable backend profiles use these modules:

- `gopkg.in/yaml.v3` v3.0.1 — MIT and Apache-2.0 Licenses (assignment config
  parsing);
- `github.com/redis/go-redis/v9` v9.7.3 — BSD 2-Clause License (used only by
  the `postgres-redis` profile);
- `github.com/segmentio/kafka-go` v0.4.47 — MIT License (used only by the
  `postgres-kafka` profile);
- `github.com/cespare/xxhash/v2` v2.2.0 — MIT License (indirect);
- `github.com/dgryski/go-rendezvous`
  v0.0.0-20200823014737-9f7001d12a5f — MIT License (indirect);
- `github.com/klauspost/compress` v1.15.9 — BSD 3-Clause License (indirect);
- `github.com/pierrec/lz4/v4` v4.1.15 — BSD 3-Clause License (indirect);
- `github.com/xdg-go/scram`, `github.com/xdg-go/stringprep`, and
  `github.com/xdg-go/pbkdf2` — Apache-2.0 License (indirect, Kafka SASL).

The Redis and Kafka client modules are compiled into artifacts only when their
respective profiles are built and selected. Module source, exact checksums, and
the selected dependency graph are recorded by `go.mod` and `go.sum`. License
texts are distributed by their upstream module sources.

The Jack Christensen modules above are distributed under this license:

> MIT License
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to
> deal in the Software without restriction, including without limitation the
> rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
> sell copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in
> all copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.

## WAAS external behavioral reference

WAAS is an external project under the MIT License and may be consulted only as
a behavioral reference. It is not a dependency or component of this
repository. No WAAS source code has been imported, copied, translated, or
adapted into this initial repository.

Any future use of WAAS code must be deliberately approved and recorded here
and in [`docs/provenance.md`](docs/provenance.md), including the public source
location, exact revision, files or portions used, license, and modifications.
