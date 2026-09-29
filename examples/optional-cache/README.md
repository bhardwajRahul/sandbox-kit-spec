# Optional pip cache

This declaration-only mixin couples a persistent cache volume with pip's
configuration file. A runtime that rejects either request skips the whole
optional group, including the file write. It does not install pip.

Compose it with a workload that uses `/home/agent` as its home and has
pip installed. Existing pip configuration is preserved. Skipping the
group on recreation does not remove configuration left on a reused home
volume.

Build against the current frontend with
`task kit:dev KIT=optional-cache`. This example needs a runtime supporting
capability groups; older strict readers reject its descriptor.
