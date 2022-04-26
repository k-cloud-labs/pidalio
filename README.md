# pidalio
![pidalio-logo](docs/images/pidolio.png)

[![Build Status](https://github.com/k-cloud-labs/pidalio/actions/workflows/ci.yml/badge.svg)](https://github.com/k-cloud-labs/pidalio/actions?query=workflow%3Abuild)
[![codecov](https://codecov.io/gh/k-cloud-labs/pidalio/branch/main/graph/badge.svg?token=74uYpOiawR)](https://codecov.io/gh/k-cloud-labs/pidalio)
[![Go Report Card](https://goreportcard.com/badge/github.com/k-cloud-labs/pidalio)](https://goreportcard.com/report/github.com/k-cloud-labs/pidalio)
[![Go doc](https://img.shields.io/badge/go.dev-reference-brightgreen?logo=go&logoColor=white&style=flat)](https://pkg.go.dev/github.com/k-cloud-labs/pidalio)

A transport middleware working in clientside for client-go to mutate any k8s resource via (Cluster)OverridePolicy .  
If you want to use it in serverside as a webhook, please use https://github.com/k-cloud-labs/kinitiras.


## Quick Start
What you need to do is just call `Wrap` func after `rest.Config` initialized and before client to initialize.

```go
config.Wrap(pidalio.NewPolicyTransport(config, stopCh).Wrap)
```

## Feature
- [x] Support mutate k8s resource by (Cluster)OverridePolicy via plaintext jsonpatch.
- [ ] Support mutate k8s resource by (Cluster)OverridePolicy programmable via cue.