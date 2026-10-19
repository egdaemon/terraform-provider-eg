terraform {
  required_providers {
    eg = {
      source = "egdaemon/eg"
    }
  }
}

provider "eg" {
  # account_id = "00000000-0000-0000-0000-000000000000"
}

# run the repository's tests/stress workload every weekday.
resource "eg_repository_scheduled_workload" "default" {
  repository = "git@github.com:egdaemon/eg.git"
  entry      = "tests/stress"
  cron       = "1d @ on weekdays"
  timeout    = "1h"
  memory     = "1g"
  cpu        = 8
}
