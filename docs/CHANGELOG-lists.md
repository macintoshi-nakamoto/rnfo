# Target list change log

Every change to what is measured. A row in the dataset can be traced to its exact
target set through the `list_manifest` field in the run record; this file explains why
the set changed.

| Date | Change | Upstream commit | Redeployed |
|---|---|---|---|
| 2026-09-04 | Initial pin: Citizen Lab `global` (1725 URLs) and `ru` (1092 URLs), plus 10 connectivity controls. | `da9219f56c9ec29a2999bbf2ffb023159df3fe9c` | ru-msk-vps |
| 2026-09-04 | Controls re-categorised `CTRL-RU` / `CTRL-INTL` (no URL change). Added the `own` list: our responder on four TLS ports, dialled by address. Written per probe from `.env` by the deploy tool; the address is not in git. | - | ru-msk-vps, nl-lim-panel, de-fra-vps |
