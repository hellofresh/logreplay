logreplay
===========

The overall goal is to re-feed logs stored on S3 into an ElasticSearch (ES) cluster.
The resulting binary is meant to be used as the value for `ENTRYPOINT` in [this Dockerfile](https://github.com/hellofresh/logreplay-docker).

This code handles following tasks:
  - writing template files based on values provided via environment variables,
  - mounting an AWS S3 bucket with the help of [s3fs-fuse](https://github.com/s3fs-fuse/s3fs-fuse),
  - starting the [Filebeat service](https://www.elastic.co/products/beats/filebeat) to feed the logs into an ES cluster.

