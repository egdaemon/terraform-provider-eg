provider "eg" {
  # account_id   = "..."           # defaults to EG_ACCOUNT
  # ssh_key_path = "~/.ssh/eg"     # defaults to the key the eg cli uses
}

resource "eg_repository_scheduled_workload" "default" {
  repository = "git@github.com:egdaemon/eg.git"
  entry      = "tests/stress"
  cron       = "1d @ on weekdays"
  timeout    = "1h"
  memory     = "1g"
  cpu        = 8
}
