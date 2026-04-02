# Contributing to rune-operator

First off, thank you for considering contributing to rune-operator! It's people like you that make rune-operator such a great tool.

## Where do I go from here?

If you've noticed a bug or have a feature request, make one! It's generally best if you get confirmation of your bug or approval for your feature request this way before starting to code.

## Fork & create a branch

If this is something you think you can fix, then fork rune-operator and create a branch with a descriptive name.

## Get the test suite running

Make sure you have Go installed (see `go.mod` for the required Go version).
From the repository root, run `go test ./...` to ensure everything is working correctly before you begin.
If you use `golangci-lint`, you can also run `golangci-lint run`.

## Implement your fix or feature

At this point, you're ready to make your changes. Feel free to ask for help; everyone is a beginner at first.

## Provide a reproducible benchmark suite

If you're adding a new benchmark suite, ensure it is fully reproducible and clearly documented.

## Make a Pull Request

At this point, you should switch back to your local default branch (for example, `main` or `develop`) and make sure it's up to date. Tests should pass on your branch.

## Code of Conduct

Please note that this project is released with a Contributor Code of Conduct. By participating in this project you agree to abide by its terms. See `CODE_OF_CONDUCT.md` for more information.
