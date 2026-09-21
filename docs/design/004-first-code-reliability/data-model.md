# Data model
No migration. Code and token records are unchanged. `server.base_url` is an HTTP(S) origin, normalized by removing its optional root slash; empty disables new dynamic creation. Code `version` is copied into the edit form and advanced only from a successful update response. Peer buckets and browser UI state remain transient.
