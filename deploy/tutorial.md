# Deploy Cromwell Task Monitoring with BigQuery

<walkthrough-project-billing-setup/>

When you run our deployment script (see below),
it will ask for a few parameters:

- `REGION` is the Google Cloud region for the deployment.

- `CROMWELL_BASEURL` is the base URL of the Cromwell API,
  e.g. `https://cromwell.example.org`

- `CROMWELL_SAM_BASEURL` is the base URL of the Cromwell Sam API,
  e.g. `https://sam.example.org`

- `CROMWELL_LOGS_BUCKET` is the bucket corresponding
  to `final_workflow_log_dir` option in Cromwell.

  It **must** be in the same _project_
  as the one you selected above ({{ project-id }}).

  If you didn't have one beforehand,
  just press Enter, and we will create it for you.

- `CROMWELL_TASK_SERVICE_ACCOUNT_EMAIL` is the email
  of the Service Account used by your Cromwell task instances.

- `DATASET_ID` is the name of BigQuery dataset
  where all monitoring tables will be stored.

Hope you're ready!

Please run the following command in the shell:
```sh
PROJECT_ID={{ project-id }} ./deploy.sh
```

After the deployment is complete, the script will
instruct you what to do next.

Happy monitoring!
