# Third-Party Notices

ZenForge is an independent Go harness. It is not affiliated with, endorsed by,
or a redistribution of the DeepSeek Harness project as a whole; the notices
below record the third-party material that ZenForge serves or derives from, as
their licenses require.

## DeepSeek Harness web console

- **Project**: DeepSeek Harness (`deepseek-ai/deepseek-harness`)
- **Source**: https://github.com/deepseek-ai/deepseek-harness
- **License**: MIT — Copyright (c) 2026 DeepSeek
- **What ZenForge uses**: the browser console frontend that ZenForge will serve
  to operators once the host side of its protocol is implemented (the build
  that ships it will carry this notice with it). ZenForge implements that host
  side in Go, inside this repository; the host implementation is ZenForge's own
  work and is not derived from the project's host code.
- **Modifications**: the console will be presented under the ZenForge name — its
  page title, logo, and brand strings replaced — and served by the `zenforge
  serve` host. Attribution and this notice are retained as the license
  requires.

The upstream console reports itself under its own product name when built from
its own sources; ZenForge's rebranding is a downstream modification permitted by
the MIT license and does not imply that the upstream project produced, reviewed,
or endorses this build.

## MIT License (as required by the notice above)

```
MIT License

Copyright (c) 2026 DeepSeek

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Adding to this file

Any further third-party code, asset, or protocol implementation that ZenForge
ships — including a bundled console build and any dependency whose license
requires attribution — is recorded here with its source, license, and the
nature of the use, in the same shape as the entry above. Vendored code keeps its
own license file next to it, and this notice points at it.