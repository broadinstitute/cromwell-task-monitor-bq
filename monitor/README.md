## Monitoring image/script

This module implements Cromwell `monitoring_image`
(or `monitoring_script`) in Go.

The image/script is designed to be "self-sufficient",
i.e. it assumes certain defaults for naming of dataset/tables,
and measurement/reporting intervals.

It also attempts to read reportable attributes
from runtime environment, whether it runs locally
or on a GCE instance.

Finally, it creates both the dataset and the tables
if they don't exist yet.

As a result, this code can be run without any
parameters, either as `monitoring_script` on PAPIv1/v2,
or as `monitoring_image` on PAPIv2. In the latter case,
it will additionally record workflow ID and task call identifiers
(which are passed to it through env vars by the Cromwell backend).
