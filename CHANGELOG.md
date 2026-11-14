# Changelog

## [0.11.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.10.0...v0.11.0) (2026-11-14)


### Features

* **usage:** log each job's model usage and stage times ([#122](https://github.com/marcomaiermm/DiffHarbor/issues/122)) ([f423546](https://github.com/marcomaiermm/DiffHarbor/commit/f42354608cfce723e9cc315da5d234ea3fcba8ba))


### Bug Fixes

* reliability of reviews across restarts, slow forges and stuck processes ([#121](https://github.com/marcomaiermm/DiffHarbor/issues/121)) ([6b849fa](https://github.com/marcomaiermm/DiffHarbor/commit/6b849fa6cf4268c1e2d97979d35b61aa879d19f7))

## [0.10.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.9.0...v0.10.0) (2026-11-14)


### Features

* **forge:** new review comment layout ([#120](https://github.com/marcomaiermm/DiffHarbor/issues/120)) ([a86a320](https://github.com/marcomaiermm/DiffHarbor/commit/a86a3208e66b623c558aca9b9ac0c33ebc5fc5e6))
* **report:** resolve calls through interfaces, callbacks and stores ([#118](https://github.com/marcomaiermm/DiffHarbor/issues/118)) ([7cfbd15](https://github.com/marcomaiermm/DiffHarbor/commit/7cfbd1538966a4951da515a521174733694c6945))
* **review:** leaner review prompt, wording rules for model text ([#119](https://github.com/marcomaiermm/DiffHarbor/issues/119)) ([2aef2a3](https://github.com/marcomaiermm/DiffHarbor/commit/2aef2a36e82746fae0eda1498c9353ce97ff1fd9))


### Bug Fixes

* **gitlab:** escape "$" in prose so paths render as text, not math ([#115](https://github.com/marcomaiermm/DiffHarbor/issues/115)) ([5ab557b](https://github.com/marcomaiermm/DiffHarbor/commit/5ab557b118b7f08ccfe4d0a571e8e72716ddc6e7))

## [0.9.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.8.1...v0.9.0) (2026-11-11)


### Features

* **chat:** learnings keep their reason and PR, skip repeats and one-offs ([#89](https://github.com/marcomaiermm/DiffHarbor/issues/89)) ([ec9fd05](https://github.com/marcomaiermm/DiffHarbor/commit/ec9fd05d35de1995ad1b423001e2fe64c910bc01))
* **job:** count finding outcomes at close, learn from declined findings, stats ([#105](https://github.com/marcomaiermm/DiffHarbor/issues/105)) ([12027f3](https://github.com/marcomaiermm/DiffHarbor/commit/12027f340099b2c48c107a987bbd5939e4f8fc96))
* **knowledge:** reference docs from the repo, GitLab wiki, Linear and remote adapters ([#96](https://github.com/marcomaiermm/DiffHarbor/issues/96)) ([4407a9a](https://github.com/marcomaiermm/DiffHarbor/commit/4407a9aaf35ceb7e0e6419c6758bda5631171e82))
* **outcomes:** remember declined findings apart from the learnings ([#112](https://github.com/marcomaiermm/DiffHarbor/issues/112)) ([c3ad4a1](https://github.com/marcomaiermm/DiffHarbor/commit/c3ad4a1967a44acfb95a6ad8efeea8c07dc173fe))
* **outcomes:** what maintainers decided per finding, kept per finding for stats ([#108](https://github.com/marcomaiermm/DiffHarbor/issues/108)) ([c8d150c](https://github.com/marcomaiermm/DiffHarbor/commit/c8d150c56cf7ad63674e14f2b180d150ef31fac8))
* **review:** cap a run at 5 inline findings, fold the rest ([#111](https://github.com/marcomaiermm/DiffHarbor/issues/111)) ([7e52bf1](https://github.com/marcomaiermm/DiffHarbor/commit/7e52bf1b6a267e6f95541e3422ce66a06963b911))
* **review:** fold kept findings the verifier finds no trigger for ([#109](https://github.com/marcomaiermm/DiffHarbor/issues/109)) ([4eff63a](https://github.com/marcomaiermm/DiffHarbor/commit/4eff63a9faa5234191fb0f09aa9a0e197bcc04a4))
* **review:** invert the review model for commits another provider's AI wrote ([#100](https://github.com/marcomaiermm/DiffHarbor/issues/100)) ([9835f91](https://github.com/marcomaiermm/DiffHarbor/commit/9835f913b5003f543b07cc2dc2acfabc687f5a4b))
* **review:** place more suggested fixes, ask for them on any small local edit ([#110](https://github.com/marcomaiermm/DiffHarbor/issues/110)) ([a5d008b](https://github.com/marcomaiermm/DiffHarbor/commit/a5d008b1ed4ae8bb87d6ce161f83b50e28255170))
* **review:** repro tests for major findings, proof tests for JS and TS ([#103](https://github.com/marcomaiermm/DiffHarbor/issues/103)) ([28e0293](https://github.com/marcomaiermm/DiffHarbor/commit/28e0293030f3f76593069e2bf29e73e2e47f9978))
* **walkthrough:** merge risk 0-5 from what is still open ([#101](https://github.com/marcomaiermm/DiffHarbor/issues/101)) ([36bcfa6](https://github.com/marcomaiermm/DiffHarbor/commit/36bcfa678387d01e8b8b1a198c14d19d99639a21))


### Bug Fixes

* **deps:** build on Go 1.27.4 for GO-2026-6859 ([#114](https://github.com/marcomaiermm/DiffHarbor/issues/114)) ([947e2d1](https://github.com/marcomaiermm/DiffHarbor/commit/947e2d109f28821215b721c18c20614685d5fa3f))
* **gitlab:** the summary heads a review's findings ([#91](https://github.com/marcomaiermm/DiffHarbor/issues/91)) ([1f41ca5](https://github.com/marcomaiermm/DiffHarbor/commit/1f41ca5aec284013dbf30d5f0c69527859a1d79b))
* **gitlab:** the summary is a note of its own, not a thread to resolve ([#107](https://github.com/marcomaiermm/DiffHarbor/issues/107)) ([18052b5](https://github.com/marcomaiermm/DiffHarbor/commit/18052b501d79a192cf9acae82cf826c5be36f17b))
* **knowledge:** address review findings ([#97](https://github.com/marcomaiermm/DiffHarbor/issues/97)) ([1b871fe](https://github.com/marcomaiermm/DiffHarbor/commit/1b871fec3fbb3c3392424609e4457bfc1ac44e3e))
* **knowledge:** forge heading anchors, Linear section links and aliases ([#98](https://github.com/marcomaiermm/DiffHarbor/issues/98)) ([c4a4686](https://github.com/marcomaiermm/DiffHarbor/commit/c4a46866ee93ef5896bc28be2dbcc808b4325aa5))
* **outcomes:** a cut-off reaction list counts nothing, at most 3 learnings per close ([#106](https://github.com/marcomaiermm/DiffHarbor/issues/106)) ([f18fadf](https://github.com/marcomaiermm/DiffHarbor/commit/f18fadf7f01000dc3bc8c77fa92901e7580ea74d))
* **review:** check a finding's premise outside the diff ([#87](https://github.com/marcomaiermm/DiffHarbor/issues/87)) ([c1160fd](https://github.com/marcomaiermm/DiffHarbor/commit/c1160fd56070ad39d13faec2c0df332b14ca4c55))
* **review:** retargets, open earlier findings, regressions and pushback ([#99](https://github.com/marcomaiermm/DiffHarbor/issues/99)) ([300b983](https://github.com/marcomaiermm/DiffHarbor/commit/300b983e897c12a45415d0db5a3807ec93e29507))

## [0.8.1](https://github.com/marcomaiermm/DiffHarbor/compare/v0.8.0...v0.8.1) (2026-11-06)


### Bug Fixes

* **review:** pushes fold minor findings, severity by recovery ([#85](https://github.com/marcomaiermm/DiffHarbor/issues/85)) ([3a22256](https://github.com/marcomaiermm/DiffHarbor/commit/3a222569b30185f1b5a276cd8994259d7a2c1050))

## [0.8.0](https://github.com/marcomaiermm/DiffHarbor/compare/v0.7.1...v0.8.0) (2026-11-06)


### Features

* **gitlab:** findings, answers and summary as one review ([#73](https://github.com/marcomaiermm/DiffHarbor/issues/73)) ([197a58d](https://github.com/marcomaiermm/DiffHarbor/commit/197a58dd38a8625537a7290d159c9a93ce76b883))
* **review:** drafts setting: manual, opt_in or off ([#71](https://github.com/marcomaiermm/DiffHarbor/issues/71)) ([7ad700f](https://github.com/marcomaiermm/DiffHarbor/commit/7ad700f9637ce0ebdb9586419f36698c998453a7))
* **review:** review profiles fold findings, nits always reported ([#79](https://github.com/marcomaiermm/DiffHarbor/issues/79)) ([03b1610](https://github.com/marcomaiermm/DiffHarbor/commit/03b1610660f5289d71e42fb5bce22aaa1c598e46))
* **review:** unplaced fixes show as a diff of what they replace ([#70](https://github.com/marcomaiermm/DiffHarbor/issues/70)) ([413aa9e](https://github.com/marcomaiermm/DiffHarbor/commit/413aa9e03e5e83a911de9784694ae376c1f252f7))
* **server:** run jobs of different repos at once (MAX_JOBS) ([#81](https://github.com/marcomaiermm/DiffHarbor/issues/81)) ([e03898a](https://github.com/marcomaiermm/DiffHarbor/commit/e03898a0d4b90d7f6764749b51a9d98862480b27))


### Bug Fixes

* **chat:** concede after one pushback, keep the risk in a collapsed block ([#76](https://github.com/marcomaiermm/DiffHarbor/issues/76)) ([7543e8c](https://github.com/marcomaiermm/DiffHarbor/commit/7543e8c4de5afdb04d8bb13beab16d6b13bb4455))
* **forge:** findings the forge didn't post inline get their own heading ([#77](https://github.com/marcomaiermm/DiffHarbor/issues/77)) ([390515c](https://github.com/marcomaiermm/DiffHarbor/commit/390515cf95c61a970a4db20de19138fc02a23cae))
* **gitlab:** escape quick actions in posted notes ([#68](https://github.com/marcomaiermm/DiffHarbor/issues/68)) ([f091ed6](https://github.com/marcomaiermm/DiffHarbor/commit/f091ed62683a983e334d8b5d9f1e6112f2e056cc))
* **image:** build osv-scanner and actionlint on current Go ([#78](https://github.com/marcomaiermm/DiffHarbor/issues/78)) ([2e21045](https://github.com/marcomaiermm/DiffHarbor/commit/2e210456a3413853b9ed36e241696cd2f71ae2fd))
* **review:** a fix that is a blank line stays one ([#75](https://github.com/marcomaiermm/DiffHarbor/issues/75)) ([a9d9391](https://github.com/marcomaiermm/DiffHarbor/commit/a9d9391c90f4027c9397f5658235b9aafe4b8555))
* **review:** closed threads keep their replies ([#80](https://github.com/marcomaiermm/DiffHarbor/issues/80)) ([0827b37](https://github.com/marcomaiermm/DiffHarbor/commit/0827b378d6b9bb6ad0cb7c460c0692a12ac2cb97))
* **review:** Fence finds the longest backtick run in one pass ([#72](https://github.com/marcomaiermm/DiffHarbor/issues/72)) ([e7d46e5](https://github.com/marcomaiermm/DiffHarbor/commit/e7d46e56f520e5799079c0ee21ae655da77d5e68))
* **review:** no review for an ambiguous profile, code and quoted fences in context ([#82](https://github.com/marcomaiermm/DiffHarbor/issues/82)) ([6e7b1ce](https://github.com/marcomaiermm/DiffHarbor/commit/6e7b1ce691cf60b3b916205a41e22aba3f1d6b9e))
* **review:** pushes stop reopening declined findings ([#83](https://github.com/marcomaiermm/DiffHarbor/issues/83)) ([562a58d](https://github.com/marcomaiermm/DiffHarbor/commit/562a58dccd084dc424bd7f1cfcf5c1fd5a918bc6))
* **review:** trim a fix's trailing newline once, in Split ([#74](https://github.com/marcomaiermm/DiffHarbor/issues/74)) ([44e912a](https://github.com/marcomaiermm/DiffHarbor/commit/44e912a96b138af50e7f68fbc62c18acf9762baa))

## [0.7.1](https://github.com/marcomaiermm/DiffHarbor/compare/v0.7.0...v0.7.1) (2026-11-06)


### Bug Fixes

* checked commit under chat replies, bad repeats indices keep all findings ([#67](https://github.com/marcomaiermm/DiffHarbor/issues/67)) ([6a6c953](https://github.com/marcomaiermm/DiffHarbor/commit/6a6c953647dce726634cbd75d320fcce8c476c62))
* **review:** verifier drops repeats of open threads ([#65](https://github.com/marcomaiermm/DiffHarbor/issues/65)) ([19f2497](https://github.com/marcomaiermm/DiffHarbor/commit/19f2497b8ce9a2f319f252e88497941012cb7ba8))

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
