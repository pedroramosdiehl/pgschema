# pgschema Wrapper Assets

This folder contains helper assets for the pgschema wrapper workflow.

## Local usage

```bash
./scripts/wrapper/generate.sh --version 1.9.6 --source remote
./pgschemaw --version
```

To pre-download the binary in the same step:

```bash
./scripts/wrapper/generate.sh --version 1.9.6 --source remote --download-now
```

To use local build mode (no binary download):

```bash
./scripts/wrapper/generate.sh --version 1.9.6 --source local
./pgschemaw --version
```

## Wrapper files

- `pgschemaw` (bash runner)
- `pgschemaw.ps1` (PowerShell runner)
- `.pgschema-wrapper.properties` (version + download settings)

## Published artifacts

Release workflow packages:

- `pgschema-wrapper-<version>.zip`
- `pgschema-wrapper-<version>.tar.gz`

Both archives include the wrapper runners, a template properties file, and this README.
