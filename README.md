# Downdetector Exporter

## Usage

### Help message

Run `./downdetector-exporter --help` to display the available options. Long options
can use either one or two leading hyphens, and the short aliases are shown in the
help output. Environment variables provide defaults for the corresponding flags.

#### Prerequisites

Either provide a credentials file using the --credentials_file parameter. The file needs to be in YAML format like this:

```
---
username: adjshkajsdhakjsd
password: djhfksjdfhksjfhksjdhf
company_ids: "123,456"
```
Company IDs may also be provided with the `--company_ids` flag or `COMPANY_IDS`
environment variable; those values override the YAML value. Username and password
flags likewise take precedence over the values in the credentials file.

### How to execute

There are two modes - either you already know your company's IDs, then provide them in a comma separated list:

`downdetector-exporter -c downdetector-credentials.yaml -i 23456,12345,34567`

This will startup the exporter in production mode. Check on port 9313.

The other mode is if you not yet know IDs of your company. In this case just pass a search_string parameter which will be searched for case insensitively:

`downdetector-exporter -c downdetector-credentials.yaml -s microsoft`

This will render a list of IDs you can chose yours from:

```
ID: 37466 - Name: Microsoft Azure, Slug: windows-azure, Country: MX
ID: 37467 - Name: Microsoft Azure, Slug: windows-azure, Country: AR
ID: 38018 - Name: Microsoft Teams, Slug: teams, Country: US
ID: 38184 - Name: Microsoft Teams, Slug: teams, Country: GB
ID: 38185 - Name: Microsoft Teams, Slug: teams, Country: DE
ID: 38186 - Name: Microsoft Teams, Slug: teams, Country: NL
ID: 38187 - Name: Microsoft VLSC, Slug: vlsc, Country: US
ID: 38694 - Name: Microsoft Teams, Slug: teams, Country: AU
ID: 39143 - Name: Microsoft Azure, Slug: windows-azure, Country: NZ
ID: 39144 - Name: Microsoft Teams, Slug: teams, Country: NZ
ID: 39145 - Name: Microsoft Teams, Slug: teams, Country: JP
ID: 39146 - Name: Microsoft Teams, Slug: teams, Country: CA
ID: 39302 - Name: Microsoft Teams, Slug: teams, Country: SE
ID: 39317 - Name: Microsoft Azure, Slug: windows-azure, Country: SE
ID: 39318 - Name: Microsoft Azure, Slug: windows-azure, Country: FI
```

### Running with systemd

Systemd can restart the exporter after a failure with a unit file such as:

```
[Unit]
Description=Prometheus Downdetector Exporter
After=network.target
AssertPathExists=/home/username/go/bin

[Service]
Type=simple
ExecStart=/home/username/go/bin/downdetector-exporter -c downdetector-credentials.yaml -i 23456,12345,34567
User=username
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

### Configure via environment variables

To make the system more compatible with docker and kubernetes, all parameters (except the search string) can be set via environment variables:

```
COMPANY_IDS
CREDENTIALS_FILE
DD_USERNAME
DD_PASSWORD
LISTEN_ADDRESS
METRICS_PATH
LOG_LEVEL
```

#### Running in Docker Compose

Example of running in docker compose using some the above variables

```
services:
  downdetector-exporter:
    container_name: downdetector-exporter
    image: ghcr.io/themeier/downdetector-exporter
    restart: always
    environment:
      - COMPANY_IDS=23456,12345,34567
      - DD_USERNAME=client-id
      - DD_PASSWORD=secret-token
```

#### Running in Kubernetes

Example of running in kubernetes:

```
apiVersion: apps/v1
kind: Deployment
metadata:
  name: downdetector-exporter
spec:
  replicas: 1
  strategy: {}
  template:
    spec:
      containers:
        - env:
            - name: COMPANY_IDS
              value: "23456,12345,34567"
            - name: DD_USERNAME
              value: client-id
            - name: DD_PASSWORD
              value: secret-token
          image: ghcr.io/themeier/downdetector-exporter
          name: downdetector-exporter
          resources: {}
      restartPolicy: Always
status: {}
```

### Grafana dashboard

There's a grafana_dashboard.json file in the repository which can be imported to Grafana.
