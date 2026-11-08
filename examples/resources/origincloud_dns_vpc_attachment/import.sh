# A VPC attachment is imported by its resource name, whose last segment is
# the VPC's id.
terraform import origincloud_dns_vpc_attachment.main projects/my-project-123/hostedZones/hz-XEkE66gA3ma/vpcAttachments/vpc-1a2b3c
