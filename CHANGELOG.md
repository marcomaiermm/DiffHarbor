# Changelog

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
