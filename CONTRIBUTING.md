# Contributing

Thank you for helping improve Origin Cloud's provider for Terraform and
OpenTofu.

## Issues

Open an issue for anything that is wrong, unclear or missing: an attribute
that behaves differently from its description, a plan that changes when nothing
did, a resource you need. Say which resource or data source it is about, and
include the configuration and the output of `terraform plan` or `apply` where
you can, with tokens and account details removed.

To report a security vulnerability, don't open an issue: use **Report a
vulnerability** on this repository's Security tab, which reaches us privately.

## Pull requests

The code here is published from Origin Cloud's own source: the resources and
data sources are generated from the API definitions, and the rest is
maintained next to the generator. A pull request in this repository is
therefore never merged. If it describes a change we want, we make the change
at the source, credit you, and close the pull request; the change then arrives
here with the next published commit.

A pull request is a good way to show exactly what you mean. For anything larger
than a comment fix, open an issue first, so that we can agree on the change
before you spend time on it.

## License

By contributing, you agree that your contribution is licensed under the
Mozilla Public License 2.0, like the rest of this repository.
