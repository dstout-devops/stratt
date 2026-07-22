package awss3

import (
	"encoding/json"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// bucketArn renders the region-independent S3 ARN identity for a bucket name.
func bucketArn(name string) string { return "arn:aws:s3:::" + name }

// normalizeBucket maps observed bucket METADATA to a `bucket` Entity (identity
// aws.bucketArn, Facet bucket.config). Pure content-expertise; NEVER object bytes
// (§1.2). versioning ("" if unknown/unsupported) and managed (the stratt:managed marker)
// are best-effort enrichment the Syncer gathered.
func normalizeBucket(region, name string, creationDate *time.Time, versioning string, managed bool) *pluginv1.ObservedEntity {
	labels := map[string]string{"aws.region": region}
	if managed {
		labels["stratt.managed"] = "true"
	}
	doc := map[string]any{}
	if creationDate != nil {
		doc["creationDate"] = creationDate.UTC()
	}
	if versioning != "" {
		doc["versioning"] = versioning
	}
	facets := map[string][]byte{}
	if len(doc) > 0 {
		if raw, err := json.Marshal(doc); err == nil {
			facets["bucket.config"] = raw
		}
	}
	return &pluginv1.ObservedEntity{
		Kind:         "bucket",
		IdentityKeys: map[string]string{"aws.bucketArn": bucketArn(name)},
		Labels:       labels,
		Facets:       facets,
	}
}
