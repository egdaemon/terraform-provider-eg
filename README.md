### EG Daemon's terraform provider

provides IaC functionality to egdaemon ci/cd.

### provider configuration

the provider authenticates to the eg api the same way the `eg` cli does, as the profile the ssh key is registered to. the key must already exist, register it with the cli first.

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
  # also the default label of eg_gcp_identity_federation connections.
  account_id = "00000000-0000-0000-0000-000000000000"

  # optional. path to the ssh private key, defaults to the eg cli's key.
  # ssh_key_path = "~/.config/eg/id_ed25519"

  # optional. allow unsigned tls certificates, defaults to $EG_COMPUTE_TLS_INSECURE.
  # insecure = false
}
```

the api host is fixed when the provider is built, the same as the cli.

### eg_gcp_identity_federation

registers a [workload identity federation](https://cloud.google.com/iam/docs/workload-identity-federation) provider with eg so workloads can use the gcp project it belongs to, see the gcp access documentation for setting up the project. managing connections requires the user management permission. the google side is managed with the google provider's resources, this resource is only the connection.

```hcl
resource "eg_gcp_identity_federation" "default" {
  audience                    = "//iam.googleapis.com/${google_iam_workload_identity_pool_provider.egdaemon.name}"
  impersonate_service_account = google_service_account.egdaemon.email
  description                 = "my project"
}
```

| attribute | |
|---|---|
| `audience` | required. the full resource name of the workload identity pool provider. |
| `impersonate_service_account` | optional. service account email workloads impersonate after the token exchange. |
| `description` | optional. |
| `label` | optional. identifies the connection within the account, defaults to the account id. changing it replaces the connection. |
| `id` | the label. |

creating a connection fails if the label already exists, rather than overwriting it. adopt an existing one with `terraform import eg_gcp_identity_federation.default <label>`.

when `account_id` is not configured the label is chosen by the api on create (the account id) and an existing connection can't be detected beforehand, set `account_id` or a `label` to get the check.

### eg_aws_identity_federation

registers an aws iam role with eg so workloads can assume it through web identity federation, see the aws access documentation for setting up the account. managing connections requires the user management permission. the aws side is managed with the aws provider's resources, this resource is only the connection.

```hcl
resource "eg_aws_identity_federation" "default" {
  role_arn    = aws_iam_role.egdaemon.arn
  region      = "us-east-2"
  description = "my account"
}
```

| attribute | |
|---|---|
| `role_arn` | required. the arn of the iam role workloads assume. |
| `region` | optional. the default region of the workloads. |
| `audience` | optional. the audience of the identity tokens, defaults to `sts.amazonaws.com`, it must match the client id of the iam identity provider. |
| `description` | optional. |
| `label` | optional. identifies the connection within the account, defaults to the account id. changing it replaces the connection. |
| `id` | the label. |

the behavior of the label and import is that of `eg_gcp_identity_federation`.

### data.eg_aws_oidc

the identity provider and subject the aws account trusts to accept the workloads of the eg account. requires the user management permission.

```hcl
data "eg_aws_oidc" "default" {}
```

| attribute | |
|---|---|
| `issuer_url` | the url of the issuer, the url of the `aws_iam_openid_connect_provider`. |
| `subject` | the subject of the identity tokens of the account, pin it with the `sub` condition in the trust policy of the role. |

### eg_repository_scheduled_workload

runs a repository's workload on a schedule. managing schedules requires the repository modify permission.

```hcl
resource "eg_repository_scheduled_workload" "default" {
  repository = "git@github.com:egdaemon/eg.git"
  workload   = "tests/stress"
  schedule   = "1d @ on weekdays"
  timeout    = "1h"
  memory     = "1g"
  cpu        = 8
}
```

| attribute | |
|---|---|
| `repository` | required. canonical vcs uri of the repository, e.g. `git@github.com:egdaemon/eg.git`. |
| `workload` | required. workload module to run within the repository. |
| `schedule` | required. [fron](https://github.com/egdaemon/fron) schedule, e.g. `1d`, `6h`, `1d @ on weekdays`. pins (`@ 03:00`) are ignored: each schedule runs at a stable slot within its frequency. |
| `timeout` | optional. maximum runtime as a duration like `30m` or `1h30m`, defaults to `1h`. |
| `cpu`, `memory`, `vram` | optional. resources to make available, sizes like `1g` or `512MiB`. |
| `arch`, `os` | optional. machine the workload runs on. |
| `labels` | optional. labels a runner must have to run the workload. |
| `secret_uris` | optional. references to the secrets made available to the workload, e.g. `gcpsm://project-id/secret-name`. the secrets are resolved by the runner. |
| `description` | optional. |
| `id`, `account_id`, `created_at` | computed. |

adopt an existing schedule with `terraform import eg_repository_scheduled_workload.default <id>`.
