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
