# Contributing

Thanks for taking the time to contribute to nix-direnv-cdi.

## License

This project is licensed under the Apache License, Version 2.0. By submitting a
contribution, you agree that your contribution is licensed under the same
Apache-2.0 terms, unless you explicitly state otherwise in writing.

Copyright 2026 Andreja Nesic.

Legal/contact email: office@andrejanesic.com.

## Development

Before sending a change, run the fast test suite:

```sh
go test -short ./...
```

Logic changes to `hook`, `nsmount`, or `cdispec` should also be checked against
real podman when possible, because important runtime behavior only shows up in a
live container.
