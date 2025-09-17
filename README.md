# gdrivetoolbox

A Go toolbox for automating Google Drive PDF deployments, version management, and Drive API authentication.

## Features

- **DeployPDF**: Uploads a PDF to Google Drive, handles versioning, and optionally archives or deletes old versions.
- **CheckRemoteVersionExists**: Checks if a specific version of a PDF is already deployed in a Drive folder.
- **UploadFileToDrive**: Uploads any file to a specified Drive folder using the Drive API.
- **GetGoogleAccessToken**: Exchanges a refresh token for a Google OAuth2 access token.

## Requirements

- Go 1.21 or newer
- Google Drive API enabled and OAuth2 credentials (client ID, client secret, refresh token)

## Installation

Clone the repo:

```sh
git clone https://github.com/hwalton/gdrivetoolbox.git
cd gdrivetoolbox
```

Build:

```sh
go build ./...
```

## Usage

### Deploy a PDF

```go
import "github.com/hwalton/gdrivetoolbox/deploy"

err := deploy.DeployPDF(
    accessToken,      // Google OAuth2 access token
    "mydoc",          // File name (without .pdf)
    "v1.2.3",         // Version string
    "tempFolderID",   // Temporary Drive folder ID
    "finalFolderID",  // Final Drive folder ID
    "archiveFolderID",// (Optional) Archive folder ID for old versions
    "/path/to/pdfs",  // Directory containing PDFs
)
if err != nil {
    log.Fatal(err)
}
```

### Check if a version exists

```go
exists, err := deploy.CheckRemoteVersionExists(
    accessToken,
    "mydoc",
    "finalFolderID",
    "v1.2.3",
)
```

### Upload any file

```go
id, err := deploy.UploadFileToDrive(
    accessToken,
    "folderID",
    "/path/to/file.txt",
)
```

### Get Google Access Token

```go
import "github.com/hwalton/gdrivetoolbox/auth"

token, err := auth.GetGoogleAccessToken(
    clientID,
    clientSecret,
    refreshToken,
)
```

## Testing

Run all tests:

```sh
go test ./...
```

## License

Apache 2.0 - see [LICENSE](LICENSE)