# Changelog

## [0.3.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.2.0...v0.3.0) (2026-11-04)


### Features

* **agent:** dry prints tokens and cost per pass ([#37](https://github.com/marcomaiermm/DiffHarbor/issues/37)) ([8dcfa4f](https://github.com/marcomaiermm/DiffHarbor/commit/8dcfa4f66094fba69f5fafefc59e8be029613372))
* **checks:** read npm installs from private registries through the vault ([#27](https://github.com/marcomaiermm/DiffHarbor/issues/27)) ([8e81805](https://github.com/marcomaiermm/DiffHarbor/commit/8e81805824455720e5d19aa611582189d5f43873))
* **diff:** cut a file past 60k at hunks, skip generated code by its header ([#39](https://github.com/marcomaiermm/DiffHarbor/issues/39)) ([e3a912b](https://github.com/marcomaiermm/DiffHarbor/commit/e3a912bd0b6389a150c2c5655cbc9c46d4659eab))
* **report:** the PR report as the design draws it ([#34](https://github.com/marcomaiermm/DiffHarbor/issues/34)) ([6104ebe](https://github.com/marcomaiermm/DiffHarbor/commit/6104ebeef50a64baf84a6900cec995b288a258b3))
* **review:** auto-review only PRs into the default branch or base_branches ([#30](https://github.com/marcomaiermm/DiffHarbor/issues/30)) ([58c5ceb](https://github.com/marcomaiermm/DiffHarbor/commit/58c5ceba14f6292ccc4936c486fd169b3428f89e))
* **review:** every defect a pass can point to, a second look in the same session, verify at high ([#40](https://github.com/marcomaiermm/DiffHarbor/issues/40)) ([01f9412](https://github.com/marcomaiermm/DiffHarbor/commit/01f9412ac3596ce20ba5329833c2b1f194c1af23))
* **review:** large PRs in parts by default, closer to CodeRabbit's recall ([#36](https://github.com/marcomaiermm/DiffHarbor/issues/36)) ([4248c08](https://github.com/marcomaiermm/DiffHarbor/commit/4248c080f6c4e926423f6a810fd6fa6b203599dd))


### Bug Fixes

* **agent:** send Anthropic effort in output_config behind a gateway ([#32](https://github.com/marcomaiermm/DiffHarbor/issues/32)) ([f07ca6d](https://github.com/marcomaiermm/DiffHarbor/commit/f07ca6d866702abfda33d002755eb594cb2a1dab))
* **forge:** match PR comments to the design ([#33](https://github.com/marcomaiermm/DiffHarbor/issues/33)) ([0c05269](https://github.com/marcomaiermm/DiffHarbor/commit/0c052690f429fa5ab4d23c5d71ce9678ddc48881))
* **proc:** bound reads of PR-controlled files ([#28](https://github.com/marcomaiermm/DiffHarbor/issues/28)) ([cf196d7](https://github.com/marcomaiermm/DiffHarbor/commit/cf196d7b27a78b0d316b4e199d6a8ef546b00b32))
* **report:** a finding without a body shows no empty paragraph under its title ([#35](https://github.com/marcomaiermm/DiffHarbor/issues/35)) ([2cf022b](https://github.com/marcomaiermm/DiffHarbor/commit/2cf022b9088d9e5464ba4f525556ab0177a8179b))


### Performance Improvements

* **diff:** pack stylesheets after code and tests ([#38](https://github.com/marcomaiermm/DiffHarbor/issues/38)) ([23997e0](https://github.com/marcomaiermm/DiffHarbor/commit/23997e08b67bee54f577c8f826fa90fb34978288))

## [0.2.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.1.1...v0.2.0) (2026-11-04)


### Features

* **security:** harden the review sandbox, keys and supply chain ([#16](https://github.com/marcomaiermm/DiffHarbor/issues/16)) ([3956989](https://github.com/marcomaiermm/DiffHarbor/commit/395698978da22d633dea475190ba1ee437c0b64b))
* **security:** keep model keys and git tokens in a root-held proxy ([3956989](https://github.com/marcomaiermm/DiffHarbor/commit/395698978da22d633dea475190ba1ee437c0b64b))


### Bug Fixes

* **chat:** answer only about the PR ([#14](https://github.com/marcomaiermm/DiffHarbor/issues/14)) ([7c0258b](https://github.com/marcomaiermm/DiffHarbor/commit/7c0258bdb015e08eb9df0bf6e5de811f0eb64f7a))
* **deps:** update dependency sharp to ^0.35.0 [security] ([#19](https://github.com/marcomaiermm/DiffHarbor/issues/19)) ([cf26724](https://github.com/marcomaiermm/DiffHarbor/commit/cf26724f9c98771cd2587e724afa81d49c3ff340))

## [0.1.1](https://github.com/marcomaiermm/DiffHarbor/compare/v0.1.0...v0.1.1) (2026-11-03)


### Bug Fixes

* **report:** read whole signatures, not their first line ([#12](https://github.com/marcomaiermm/DiffHarbor/issues/12)) ([dec1d28](https://github.com/marcomaiermm/DiffHarbor/commit/dec1d28be2086c54908a6ee47607c7aa6b25cc78))


### Performance Improvements

* **report:** compile the CSS at build time and shrink big PRs' pages ([#11](https://github.com/marcomaiermm/DiffHarbor/issues/11)) ([d7cc674](https://github.com/marcomaiermm/DiffHarbor/commit/d7cc6740618a75a2694086d439ebbd3c8350166d))

## 0.1.0 (2026-11-03)


### Features

* add an optional SeaweedFS service for the reports ([08b85ac](https://github.com/marcomaiermm/DiffHarbor/commit/08b85aca2e3ff900d65a7b20672d7aadff755be1))
* brand the report with TEMPLATE_DIR ([00cec79](https://github.com/marcomaiermm/DiffHarbor/commit/00cec792448ce8e51938dfe7fc80fa39e60e7d0a))
* keep reports in an S3 bucket with REPORT_S3_URL ([8abbcbb](https://github.com/marcomaiermm/DiffHarbor/commit/8abbcbb1851e3aeef04ffae073cd315f630e0838))
* **ticket:** add Linear as a ticket provider ([f443f04](https://github.com/marcomaiermm/DiffHarbor/commit/f443f04ec0edaab13cf86c98547a4e4c1c39cb61))


### Bug Fixes

* address the S3 review ([fe392fe](https://github.com/marcomaiermm/DiffHarbor/commit/fe392fe6ffc2ae72f5eefe7244ab0794ffcdc5e1))
* delete reports in the worker, retried, and move disk reports to S3 ([a1d83ca](https://github.com/marcomaiermm/DiffHarbor/commit/a1d83ca94567da707b8f8ad81da7a4493ae478dc))
* stream reports to the browser again ([eb20087](https://github.com/marcomaiermm/DiffHarbor/commit/eb200875f8724b768f18e5cc69ff9bd82e43d0e1))
