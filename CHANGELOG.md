## [Omni Proxmox Infra Provider 0.1.0](https://github.com/siderolabs/omni-infra-provider-proxmox/releases/tag/v0.1.0) (2026-05-22)

Welcome to the v0.1.0 release of Omni Proxmox Infra Provider!



Please try out the release binaries and report any issues at
https://github.com/siderolabs/omni-infra-provider-proxmox/issues.

### Contributors

* Artem Chernyshev
* Arnold Mendez
* Brent
* CppBunny
* Mitch Ross
* Moritz Renker
* Tyler Ault
* netshad0w

### Changes
<details><summary>22 commits</summary>
<p>

* [`8e3dd5d`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/8e3dd5dabf7be48368a939b6b81fe0e7dad707ef) feat: configurable firewall on primary NIC via network_firewall
* [`eec4f31`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/eec4f319dcd3474729b2fba2524a145bf4080c79) feat: skip offline nodes during provisioning to prevent API errors
* [`23ace4a`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/23ace4a451567fda34b61de98eba1b3bfe93f680) feat: support vm tags and pool in machine class config
* [`144fd6c`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/144fd6c859825a33ae9ba125dffda12335418295) fix: deduplicate ISO uploads
* [`a4187e1`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/a4187e1e697d543e0dd194969d72fa6b64f14c36) test: add integration tests
* [`f5a527c`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/f5a527c2a86347907342847331f58e7968b1defb) chore: bump deps
* [`eb6f533`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/eb6f533257cbec905963bad6a80df6d83325ca6f) feat: add support for pcie mdev
* [`0d9fd58`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/0d9fd58da9ec9de75506938c755a03f4ade28ee8) feat: make provider automatically distribute VMs across nodes
* [`559954c`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/559954c759bd5b2cf319bcc0ac8c974bdb6621bb) feat: use nocloud images instead of metal
* [`e3a4a2f`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/e3a4a2f29c58f5d04845761c39fef5160b8a09cc) feat: build multiarch docker image for the provider
* [`fb8e4b2`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/fb8e4b28ef1a89d14822222cb40c1ace30e2e170) feat: honor node field in providerdata
* [`7058f00`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/7058f00a1daca77f0abc7e5e239a4e6993bdb8b1) feat: add advanced vm options
* [`5056bf8`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/5056bf8225d1e8e10cf89ecc1d8bb33d9573bd34) feat: install `qemu-guest-agent` on each machine created by provider
* [`f1daa55`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/f1daa556c693e258d3fd22da67fd0ac5810919e4) fix: use unique patch name for the hostname patches
* [`e7248ed`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/e7248ed49a700628b3120cc155039227eb118d49) chore: use machine request id as a hostname for the created nodes
* [`755fa1d`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/755fa1d1ca2ab0b45c4a5aa5eb959891d8058faf) fix: ignore not found machines during deprovisioning
* [`c71a31b`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/c71a31bff1c3406d45f9c9656c49a6d193d6aa41) feat(networking): allow customization of VM networking
* [`7e393fd`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/7e393fd1026f494976256e95eca0c005d73ecdb8) fix: bump Omni client library
* [`8cd7603`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/8cd76037d3484d732d03f83e86d88e99165a627a) fix: make `storage_selector` required
* [`b663c40`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/b663c40c207ecaeb4fee8f136c864f664710b682) docs: extend readme with reqs, docker compose, and setup helpers
* [`410ed22`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/410ed2289e2621eafc69d7c2a68ce14739ffc118) chore: rekres, bump deps
* [`da2f853`](https://github.com/siderolabs/omni-infra-provider-proxmox/commit/da2f8535996ce2db716ca414dda48bab9ffd243e) initial commit
</p>
</details>

### Dependency Changes

This release has no dependency changes

