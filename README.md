# deploy artifact to vps with http webhook

this service allows your vps to securely deploy artifacts from github actions without exposing persistent secrets. this is how it goes (high-level):
- ci uploads an artifact and calls the webhook
- the vps pulls the artifact
- verifies its signature
- deploys it to the proper location

## configuration

this service requires two files in its working directory:

1. `areas.json`: defines deployment areas (aliases) on your vps.
2. `minisign.pub`: the public key used to verify artifact signatures.

### `areas.json`

this file prevents leaking internal filesystem paths and unauthorized writes. here's an example:

```json
{
  "repos": "/home/actions/repos",
  "swiftlink": "/home/actions/swiftlink"
}
```

basically:

* `area` from webhook payload is matched to a key in this json.
* the final deployment path is constructed as: `areas[area] + "/" + project`

## webhook payload format

the service listens on `:8080` and the endpoint is `POST /deploy`. the payload must be a json with the following structure:

```jsonc
{
  "area": "repos",                // Area alias from areas.json
  "project": "minechat",          // Subfolder under the area
  "owner": "winlogon",            // GitHub owner/org
  "repo": "minechat",             // GitHub repository
  "artifact_id": "12345678",      // GitHub Actions artifact ID
  "github_token": "ghx_ABC123...",// Temporary Actions token
  "signature": "BASE64ENCODED..." // Base64 encoded minisign signature file content
}
```

## deployment workflow (step‑by‑step)

1. ci builds the project (e.g., static site in `dist/` folder).
2. ci creates a tarball
3. ci signs the tarball with minisign: `minisign -S -m site.tar.gz`.
4. ci uploads `site.tar.gz` as a github actions artifact.
5. ci calls the webhook `POST /deploy` on your vps, passing the metadata and the base64-encoded content of `site.tar.gz.minisig` as the `signature`.
6. vps fetches the zip artifact from github using `github_token`.
7. vps extracts the `.tar.gz` from the zip and verifies its signature against `minisign.pub`.
8. vps deletes `final_path` if it exists, recreates it, and extracts the tarball contents inside.

*Result*: A clean deploy:

```
/home/actions/repos/minechat/foo/bar.html
```

## example deployment

### `areas.json`

```json
{
  "repos": "/home/actions/repos",
  "swiftlink": "/home/actions/swiftlink"
}
```

### webhook post

```json
{
  "area": "repos",
  "project": "minechat",
  "owner": "winlogon",
  "repo": "minechat",
  "artifact_id": "987654321",
  "github_token": "ghx_TEMPORARY_TOKEN",
  "signature": "BASE64_MINISIGN_SIG"
}
```

### after deployment

```
/home/actions/repos/minechat/
├── foo/
│   └── bar.html
└── ...
```

## security model

* requires a short-lived github token. no permanent secrets stored on vps.
* verified with minisign before deployment.
* vps controls `areas.json` to prevent unauthorized filesystem writes.

## roadmap

- [ ] `.service` file for systemd Linux distros
- [ ] cli flags (`flag` package)
- [ ] more verbose logging
