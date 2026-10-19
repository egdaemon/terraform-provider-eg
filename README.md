### EG Daemon's terraform provider

provides IaC functionality to egdaemon ci/cd.

### provider configuration

the provider authenticates to the eg api the same way the `eg` cli does, as the profile the ssh key is registered to. the key is read when a resource first calls the api, so configurations that only use `eg_tar` do not need one.

```hcl
terraform {
  required_providers {
    eg = {
      source = "egdaemon/eg"
    }
  }
}

provider "eg" {
  # optional. required when the ssh key is associated with more than one profile, defaults to $EG_ACCOUNT.
  account_id = "00000000-0000-0000-0000-000000000000"

  # optional. path to the ssh private key, defaults to the eg cli's key.
  # ssh_key_path = "~/.ssh/eg"

  # optional. allow unsigned tls certificates, defaults to $EG_COMPUTE_TLS_INSECURE.
  # insecure = false
}
```

the api host is fixed when the provider is built, the same as the cli.

### eg_repository_scheduled_workload

runs a repository's workload on a schedule. managing schedules requires the repository modify permission.

```hcl
resource "eg_repository_scheduled_workload" "default" {
  repository = "git@github.com:egdaemon/eg.git"
  entry      = "tests/stress"
  cron       = "1d @ on weekdays"
  timeout    = "1h"
  memory     = "1g"
  cpu        = 8
}
```

| attribute | |
|---|---|
| `repository` | required. canonical vcs uri of the repository, e.g. `git@github.com:egdaemon/eg.git`. |
| `entry` | required. workload module to run within the repository. |
| `cron` | required. [fron](https://github.com/egdaemon/fron) schedule, e.g. `1d`, `6h`, `1d @ on weekdays`. classic cron expressions are rejected, and pins (`@ 03:00`) are ignored: each schedule runs at a stable slot within its frequency. |
| `timeout` | optional. maximum runtime, defaults to `1h`. |
| `cpu`, `memory`, `vram` | optional. resources to make available, sizes like `1g` or `512MiB`. |
| `arch`, `os` | optional. machine the workload runs on. |
| `labels` | optional. labels a runner must have to run the workload. |
| `description` | optional. |
| `id`, `account_id`, `created_at`, `updated_at`, `lastrun_at`, `nextrun_at` | computed. |

adopt an existing schedule with `terraform import eg_repository_scheduled_workload.default <id>`.
