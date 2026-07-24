# The AWS network stack the enterprise scenario needs, built from the NetBox-allocated CIDR. The
# module OWNS the VPC/subnet/SG/route-table/IGW composition (ADR-0112: this is why Stratt needs no
# Intent/Vpc or route-table Entities — the module encapsulates them). Everything is tagged
# stratt:managed=true so the awsec2 Syncer enumerates + OBSERVEs it (the Facet write-back, D5).

provider "aws" {
  region     = var.region
  access_key = "test" # dev-only; against floci/localstack any creds pass (skip_* below)
  secret_key = "test"

  # floci is an AWS-API backend on a custom endpoint (ADR-0093) — the standard "tofu against
  # localstack" posture. Empty aws_endpoint ⇒ real AWS (the skip_* flags are floci-only, harmless).
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  dynamic "endpoints" {
    for_each = var.aws_endpoint == "" ? [] : [var.aws_endpoint]
    content {
      ec2 = endpoints.value
    }
  }
}

locals {
  managed_tags = {
    "stratt:managed" = "true"
    "Name"           = var.subnet_name
    "stratt:vlan"    = var.stratt_ipam_vlan_id == 0 ? "" : tostring(var.stratt_ipam_vlan_id)
  }
}

resource "aws_vpc" "this" {
  cidr_block = var.vpc_cidr
  tags       = local.managed_tags
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = local.managed_tags
}

# The subnet carved from the NetBox-allocated CIDR (ADR-0111) — the point of the whole slice.
resource "aws_subnet" "this" {
  vpc_id            = aws_vpc.this.id
  cidr_block        = var.stratt_ipam_cidr
  availability_zone = "${var.region}a"
  tags              = local.managed_tags
}

resource "aws_route_table" "this" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = local.managed_tags
}

resource "aws_route_table_association" "this" {
  subnet_id      = aws_subnet.this.id
  route_table_id = aws_route_table.this.id
}

resource "aws_security_group" "this" {
  name   = var.subnet_name
  vpc_id = aws_vpc.this.id
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
  tags = local.managed_tags
}
