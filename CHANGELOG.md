# Changelog

## [0.7.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.6.1...v0.7.0) (2026-11-05)


### Features

* dependency bumps get full tsc and an npm diff of the bumped packages ([#61](https://github.com/marcomaiermm/DiffHarbor/issues/61)) ([7886ffd](https://github.com/marcomaiermm/DiffHarbor/commit/7886ffdf8b7661830d8d6dd750b2bc0f7e9cbf98))


### Bug Fixes

* **chat:** follow-ups to [#63](https://github.com/marcomaiermm/DiffHarbor/issues/63) review ([#64](https://github.com/marcomaiermm/DiffHarbor/issues/64)) ([b86f77b](https://github.com/marcomaiermm/DiffHarbor/commit/b86f77b944d47d44d437776d0fa26df43515927b))
* **chat:** no reply to questions for others, accept deferrals, severity by worst case ([#63](https://github.com/marcomaiermm/DiffHarbor/issues/63)) ([da3f5bd](https://github.com/marcomaiermm/DiffHarbor/commit/da3f5bdcdad072eedcb58dc287910834088717ec))

## [0.6.1](https://github.com/marcomaiermm/DiffHarbor/compare/v0.6.0...v0.6.1) (2026-11-05)


### Bug Fixes

* **chat:** no 👀 on replies in threads between humans ([#59](https://github.com/marcomaiermm/DiffHarbor/issues/59)) ([61721ed](https://github.com/marcomaiermm/DiffHarbor/commit/61721edf2b028ba3acce634aa962f767f614b4cf))

## [0.6.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.5.0...v0.6.0) (2026-11-05)


### Features

* **review:** animated lighthouse while reviewing, banners as a note ([#57](https://github.com/marcomaiermm/DiffHarbor/issues/57)) ([a8264b0](https://github.com/marcomaiermm/DiffHarbor/commit/a8264b0ea69fe4307485fe528abd07113d30556a))
* **review:** lighthouse in the design system's colors, served by the server with REPORT_URL ([#58](https://github.com/marcomaiermm/DiffHarbor/issues/58)) ([0132f7e](https://github.com/marcomaiermm/DiffHarbor/commit/0132f7e9490f56d699e68220e904f2959bcb0949))
* **review:** suggest_split counts code only, count and skip configure it ([#55](https://github.com/marcomaiermm/DiffHarbor/issues/55)) ([e0188a0](https://github.com/marcomaiermm/DiffHarbor/commit/e0188a0dae31af675b1c49ba6ac52b9446505cb8))

## [0.5.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.4.1...v0.5.0) (2026-11-05)


### Features

* **chat:** learnings settings in review.json, learnings and forget commands ([#53](https://github.com/marcomaiermm/DiffHarbor/issues/53)) ([f3c3031](https://github.com/marcomaiermm/DiffHarbor/commit/f3c3031260c9d96c185cb4ab5fe03f6328058d19))
* **review:** ask about unstated assumptions, check contracts and migrations, suggest a split ([#50](https://github.com/marcomaiermm/DiffHarbor/issues/50)) ([1134adc](https://github.com/marcomaiermm/DiffHarbor/commit/1134adc0bc726e74e1c5374f8882a8d6fc1f8f5f))
* **review:** run the proof test on the base versions of the PR's code ([#54](https://github.com/marcomaiermm/DiffHarbor/issues/54)) ([c5446df](https://github.com/marcomaiermm/DiffHarbor/commit/c5446df245d4e2d5d486a94e9c1e46a96ce682df))

## [0.4.1](https://github.com/marcomaiermm/DiffHarbor/compare/v0.4.0...v0.4.1) (2026-11-05)


### Bug Fixes

* **agent:** a gateway doesn't get Pi's tool_removal blocks ([#48](https://github.com/marcomaiermm/DiffHarbor/issues/48)) ([2fcd9ff](https://github.com/marcomaiermm/DiffHarbor/commit/2fcd9ff3e8c99799a4a69f49ef5b5430bc60e0da))

## [0.4.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.3.0...v0.4.0) (2026-11-05)


### Features

* **config:** ignore takes paths back with !glob, globs get doublestar's syntax ([#44](https://github.com/marcomaiermm/DiffHarbor/issues/44)) ([d7e677b](https://github.com/marcomaiermm/DiffHarbor/commit/d7e677b722013964320514a64c9b8ad2ba74f72f))
* **ticket:** tickets the description references go into the review as context ([#42](https://github.com/marcomaiermm/DiffHarbor/issues/42)) ([9548b78](https://github.com/marcomaiermm/DiffHarbor/commit/9548b78e66cc68d306786581ae4b7b926c82a23d))


### Bug Fixes

* **review:** skip a self-hosted Renovate under any name, like a bot author ([#47](https://github.com/marcomaiermm/DiffHarbor/issues/47)) ([193acea](https://github.com/marcomaiermm/DiffHarbor/commit/193acea661a327b948d7eafe4389611664bf5d4f))

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
