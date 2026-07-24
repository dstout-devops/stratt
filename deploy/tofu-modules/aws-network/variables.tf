# Inputs. stratt_ipam_cidr is CORE-INJECTED as a -var by the opentofu plugin from the resolved ipam
# handle (ADR-0111/0112 D3) — NetBox allocated it; this module never picks a CIDR itself. The rest
# are module-level knobs (the VPC supernet, the floci/AWS endpoint + region).

variable "stratt_ipam_cidr" {
  type        = string
  description = "The subnet CIDR, allocated by the ipam provider (NetBox) and injected by the core (ADR-0111 D3)."
}

variable "stratt_ipam_vlan_id" {
  type        = number
  default     = 0
  description = "Optional VLAN id from the ipam handle (0 = none). Carried as a tag; AWS has no first-class VLAN."
}

variable "vpc_cidr" {
  type        = string
  default     = "10.30.0.0/16"
  description = "The VPC supernet the ipam-allocated subnet is carved into."
}

variable "region" {
  type        = string
  default     = "eu-west-1"
  description = "The AWS region (a scope label; against floci it is nominal)."
}

variable "aws_endpoint" {
  type        = string
  default     = "http://floci:4566"
  description = "The AWS API endpoint. Dev: the floci real-EC2 backend (ADR-0093). Empty ⇒ real AWS."
}

variable "subnet_name" {
  type        = string
  default     = "stratt-subnet"
  description = "The Name tag + the correlation label for the built subnet."
}
