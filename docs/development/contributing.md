# Contributing

Meridio-2 is developed in the open at
[github.com/Nordix/Meridio-2](https://github.com/Nordix/Meridio-2). All of the
current contributors happen to work for a single company, but the project lives
on public GitHub and we welcome outside contributions.

## Where to start

Everything happens on GitHub:

- **Bug reports and feature requests:** open a
  [GitHub issue](https://github.com/Nordix/Meridio-2/issues).
- **Code changes:** open a
  [pull request](https://github.com/Nordix/Meridio-2/pulls).

We do not currently have a mailing list, a public chat, or regular public
meetings. GitHub issues and pull requests are the way to reach us. If you are
planning a larger change, please open an issue first so we can discuss the
direction before you invest time in it.

We know this is a minimal setup. We are open to improving our contribution
process and adding communication channels as the community grows -- if you have
suggestions, open an issue.

## Making a change

1. Fork the repository and create a branch for your change.
   **Do not use `/` in branch names** -- the CI uses the branch name as a Docker
   image tag, and `/` is invalid there. Use hyphens, e.g. `fix-vip-leak`.
2. Make your change, with tests where it makes sense. See the
   [testing guide](testing.md) for the test layers and how to run them.
3. Before committing, run the checks:
   ```bash
   make check   # lint, fmt, vet, and unit tests
   ```
   You can have this run automatically via `make install-hooks` (see
   [git-hooks.md](git-hooks.md)).
4. Sign off your commits with the Developer Certificate of Origin
   ([DCO](https://developercertificate.org/)):
   ```bash
   git commit -s
   ```
   This adds a `Signed-off-by` line certifying that you wrote the code or
   otherwise have the right to submit it under the project's license.
5. Open a pull request. Describe what the change does and how you tested it.

## License

By contributing, you agree that your contributions are licensed under the
project's [Apache License 2.0](../../LICENSE).

## Project conventions

For repository layout, code-generation rules, and the kubebuilder-based
workflow, see [AGENTS.md](../../AGENTS.md) and the rest of
[docs/development/](.).
