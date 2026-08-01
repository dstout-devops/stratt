# The reserved `stratt_entities` output (ADR-0017): the ONLY governed projection path from a tofu
# build. It carries the built subnet as an Entity by IDENTITY + labels — NO Facets (v1 observations
# carry none, §1.1). Critically (ADR-0112 D5), the identity scheme is `aws.subnetId` — the SAME
# scheme the awsec2 Syncer uses (ADR-0096) — so this identity-only Entity and the Syncer's OBSERVE
# projection of the real subnet's `net.subnet` Facet co-own ONE Entity, never a duplicate. The Facet
# is the Syncer's; this output only asserts the Entity exists + its correlation labels.
#
# NO LABEL HERE MAY START WITH `stratt.` — the plugin refuses the reserved prefix outright
# (outputsToWire), so `stratt.name` sat here as an apply that would fail on its first real
# run. It survived because nothing ever executed this module. The estate's own keys —
# including the stratt.intent/singleton correlation label a build's Finding resolves on —
# arrive from the LAUNCH via the opentofu/apply overlay (ADR-0145 D1/D4), never from HCL.
# That is what keeps this one module usable by every singleton that builds from it.
output "stratt_entities" {
  description = "Reserved: Entities the core write-backs with Run provenance (ADR-0017), subnet keyed by aws.subnetId."
  value = [
    {
      # The estate declares the same kind (Intent/Subnet's projectKind). The overlay REFUSES a
      # mismatch rather than picking a winner (§2.4), so this is the module stating what it
      # believes it built and being checked against it — not a second source of truth.
      kind         = "subnet"
      identityKeys = { "aws.subnetId" = aws_subnet.this.id }
      labels = {
        source       = "opentofu"
        "net.cidr"   = aws_subnet.this.cidr_block
        "net.vpc.id" = aws_vpc.this.id
      }
    }
  ]
}

# Convenience outputs (not governed projection) — useful for the provision→configure follow-up (a
# VM placed in this subnet, then an ansible Step against it, §5.1).
output "subnet_id" {
  value = aws_subnet.this.id
}

output "vpc_id" {
  value = aws_vpc.this.id
}
