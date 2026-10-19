terraform {
  required_providers {
    eg = {
      source = "egdaemon/eg"
    }
    google = {
      source = "hashicorp/google"
    }
  }
}

provider "eg" {
  # account_id = "00000000-0000-0000-0000-000000000000"
}

provider "google" {
  project = "my-project"
}

# trust eg's identity. the pool, provider and service account stay with the google provider.
resource "google_iam_workload_identity_pool" "egdaemon" {
  workload_identity_pool_id = "egdaemon"
}

resource "google_iam_workload_identity_pool_provider" "egdaemon" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.egdaemon.workload_identity_pool_id
  workload_identity_pool_provider_id = "egd-workloads"

  attribute_mapping = {
    "google.subject"  = "assertion.sub"
    "attribute.email" = "assertion.email"
  }

  attribute_condition = "assertion.email == 'egd-workloads@egd-workloads.iam.gserviceaccount.com'"

  oidc {
    issuer_uri = "https://accounts.google.com"
  }
}

resource "google_service_account" "egdaemon" {
  account_id   = "egd-compute"
  display_name = "egdaemon workloads"
}

resource "google_service_account_iam_member" "egdaemon" {
  service_account_id = google_service_account.egdaemon.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.egdaemon.name}/attribute.email/egd-workloads@egd-workloads.iam.gserviceaccount.com"
}

# register the provider with eg.
resource "eg_gcp_identity_federation" "default" {
  audience                    = "//iam.googleapis.com/${google_iam_workload_identity_pool_provider.egdaemon.name}"
  impersonate_service_account = google_service_account.egdaemon.email
  description                 = "my project"
}
