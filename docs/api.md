# REST API and API tokens

The panel does everything through the master's REST API, and scripts can use it too: with an API token instead of a
password, e.g. to start a server from a cron job or a CI pipeline.

## API tokens

Users create their tokens on their account page (in the menu of their name in the sidebar), under **API tokens** with
**Create token**:

- **Name**, to tell the tokens apart, e.g. `Backup script`.
- **Expires after** 30 days, 90 days, a year, or never. If the settings limit how long tokens may last, only the
  lifetimes within the limit are offered, and the limit itself; the master refuses others.
- **Permissions**: all of the user's, also those the user gets later, or only some of them, e.g. only to start servers.
  Choosing one also chooses what it needs, e.g. seeing the servers one may start. Only the permissions the user has are
  offered.
- The user's **password**, and with two-factor authentication a **code** of the app.

The token, e.g. `noryx_JBSWY3DPEHPK3PXPJBSWY3DPEH`, is shown only then; the master keeps only its hash. Copy it into the
secrets of the script or a password manager. The list shows each token with its permissions, when it was created and
last used, from which address, and when it expires. **Revoke** ends it at once, without the password. Expired tokens
stay listed, so that it's clear why a script stopped working, until they are revoked. The panel lists the tokens with
`GET /api/auth/tokens`, creates one with `POST /api/auth/tokens` and revokes one with `DELETE /api/auth/tokens/<id>`,
which only a session of the panel may use.

A token acts as its user, with what both the token and the user may do at the moment of each request:

- A token with some permissions can do nothing else, also if its user may. Within them, it applies where its user has
  them, e.g. only on the servers of the user's groups.
- Changes of the user's groups apply to the tokens right away. Disabling the user revokes them, so that enabling the
  user again doesn't bring them back, and deleting the user deletes them.
- Changing the password, setting one with a setup link and turning on two-factor authentication revoke all tokens of the
  user, like they end the other sessions: the tokens may belong to whoever knew the old password.
- While the settings require two-factor authentication of a user who hasn't set it up, the user's tokens get `403` with
  the code `mfa-setup-required` too, and the user can't create tokens.
- Tokens can't sign in to the panel, and can't use the routes of the user's own account under `/api/auth/` and
  `/api/preferences`: they can't change the password or two-factor authentication, end sessions, create or revoke
  tokens, or change the language and the look of the panel.

The log names the token, by its name and ID, besides the user in every entry of what the token did. Creating and
revoking tokens is logged too.

Tokens also sign in to [SFTP](servers.md#sftp), as the password with the username of their user, e.g. for an editor or
a script that syncs configuration. There they need the permissions of the file manager: to browse and download files to
see a server and read its files, and to change files to change them. The same rules apply as to requests: the master
checks the token and its permissions for every request, so revoking it ends its open connections, and wrong tokens take
the client's attempts of signing in.

## Using the API

Send the token in the `Authorization` header. For example, list the servers, whose answer holds the `nodeId` and `id` of
each, and start one:

```sh
export NORYX_TOKEN=noryx_…
curl -fsS -H "Authorization: Bearer $NORYX_TOKEN" https://panel.example.com/api/servers
curl -fsS -X POST -H "Authorization: Bearer $NORYX_TOKEN" https://panel.example.com/api/nodes/<nodeId>/servers/<id>/start
```

- Requests and answers are JSON. A failed request answers with its HTTP status and `{"error": "…"}`, which tells why,
  sometimes with a `code`. If it is about a field of the request, `field` names it by its JSON key, with dots between
  the keys of nested objects, e.g. `{"error": "Port must be between 1024 and 65535.", "field": "port"}` or
  `"field": "newServers.memoryMb"`, so that a form can show it next to that field. Errors that the agent of a node
  finds name no field.
- `401` means that the token is missing, wrong, expired or revoked, or that its user is disabled; `403` that the token
  or its user lacks a permission. A client gets 5 wrong tokens, then one every 12 seconds, together with its attempts to
  sign in; until it has one left, `429` answers even its right tokens.
- Long actions, e.g. creating a server, answer `202 Accepted` with an operation once they take more than a second;
  `GET /api/operations/<id>` tells how it goes and how it ended.
- Logs, e.g. the console of a server at `GET /api/nodes/<nodeId>/servers/<id>/logs`, stream as Server-Sent Events. A
  stream ends after 5 minutes with the event `renew`, so that the permissions are checked again; connect again with the
  ID of the last line as `Last-Event-ID` to continue.
- `GET /api/access/me` tells which permissions a token has, where.

Browsers of other sites can't use a token: the master allows no cross-origin requests (CORS), and refuses changes that a
page of another site sends.

## Description of the API

`GET /api/openapi.json` describes every route of the running master in
[OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0): its method, path, path parameters and, in `x-permissions`, the
permissions it needs, of the token and of its user. Scoped permissions apply to the node and server of the path. Routes
without permissions answer with what the user may see, and check the permissions of what a request names, e.g. the
servers of a bulk action; `administrators` stands for routes only administrators may use. The master generates the
description from the routes it registers, so it always lists all of them and their permissions. The account page links
it, and it can be loaded into tools such as Swagger UI or Postman. It doesn't describe requests and answers yet, and
leaves out the routes of the user's own account, which tokens can't use.
