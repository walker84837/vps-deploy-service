# deploy artifact to vps with http webhook

this service allows your vps to securely deploy artifacts from github actions without exposing persistent secrets. this is how it goes (high-level):
- ci uploads an artifact and calls the webhook
- the vps pulls the artifact
- verifies its signature
- deploys it to the proper location

## configuration file

this service needs an `areas.json`, which defines deployment areas (aliases) on your vps. this is to:
- avoid leaking internal filesystem paths;
- prevent unauthorized writes.

here's an example:
```json
{
  "repos": "/home/actions/repos",
  "swiftlink": "/home/actions/swiftlink"
}
```

basically:

* `area` from webhook payload is matched to a key in this json.
* the final deployment path is constructed as:

```
final_path = areas[area] + "/" + project
```

an example:

```json
// areas.json
{
  "repos": "/home/actions/repos",
  "swiftlink": "/home/actions/swiftlink"
}

// Webhook payload
{
  "area": "repos",
  "project": "minechat",
  "owner": "winlogon",
  "repo": "minechat",
  ...
}

// Final path
/home/actions/repos/minechat
```

## webhook payload format

the webhook expects a json post with the following structure:

```jsonc
{
  "area": "repos",                // Area alias from areas.json
  "project": "minechat",          // Subfolder under the area
  "owner": "winlogon",            // GitHub owner/org
  "repo": "minechat",             // GitHub repository
  "artifact_id": "12345678",      // GitHub Actions artifact ID
  "github_token": "ghx_ABC123...",// Temporary Actions token
  "signature": "BASE64ENCODED..." // Minisign signature of the artifact
}
```

a few notes:

1. `owner` + `repo` tells the VPS which GitHub repository to fetch from.
2. `artifact_id` is used to fetch the artifact from github.
3. `github_token` is the *temporary token* to authenticate the artifact download.
4. `signature` ensures integrity. the vps verifies it before deploying.

## deployment workflow (step‑by‑step)

1. ci builds the project (e.g., static site in `site/` folder).
2. ci creates a tarball: `site.tar.gz`.
3. ci signs the tarball with minisign: `site.tar.gz.minisig`.
4. ci uploads both files as github actions artifacts.
5. ci calls the webhook on your vps, passing `owner`, `repo`, `artifact_id`, `area`, `project`, and `signature`.
6. vps fetches the artifact using `github_token`.
7. vps verifies the signature.
8. vps deletes `final_path` if it exists, recreates it, and extracts the tarball inside.

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
