package azurerm

import (
	"fmt"
	"log"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-06-01/compute"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/tf"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmSnapshot() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmSnapshotCreateUpdate,
		Read:   resourceArmSnapshotRead,
		Update: resourceArmSnapshotCreateUpdate,
		Delete: resourceArmSnapshotDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validateSnapshotName,
			},

			"location": azure.SchemaLocation(),

			"resource_group_name": azure.SchemaResourceGroupName(),

			"create_option": {
				Type:     schema.TypeString,
				Required: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(compute.Copy),
					string(compute.Import),
				}, true),
				DiffSuppressFunc: ignoreCaseDiffSuppressFunc,
			},

			"source_uri": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"source_resource_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"storage_account_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"disk_size_gb": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
			},

			"encryption_settings": encryptionSettingsSchema(),

			"tags": tagsSchema(),
		},
	}
}

func resourceArmSnapshotCreateUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).compute.SnapshotsClient
	ctx := meta.(*ArmClient).StopContext

	name := d.Get("name").(string)
	resourceGroup := d.Get("resource_group_name").(string)
	location := azure.NormalizeLocation(d.Get("location").(string))
	createOption := d.Get("create_option").(string)
	tags := d.Get("tags").(map[string]interface{})

	if requireResourcesToBeImported && d.IsNewResource() {
		existing, err := client.Get(ctx, resourceGroup, name)
		if err != nil {
			if !utils.ResponseWasNotFound(existing.Response) {
				return fmt.Errorf("Error checking for presence of existing Snapshot %q (Resource Group %q): %+v", name, resourceGroup, err)
			}
		}

		if existing.ID != nil && *existing.ID != "" {
			return tf.ImportAsExistsError("azurerm_snapshot", *existing.ID)
		}
	}

	properties := compute.Snapshot{
		Location: utils.String(location),
		SnapshotProperties: &compute.SnapshotProperties{
			CreationData: &compute.CreationData{
				CreateOption: compute.DiskCreateOption(createOption),
			},
		},
		Tags: expandTags(tags),
	}

	if v, ok := d.GetOk("source_uri"); ok {
		properties.SnapshotProperties.CreationData.SourceURI = utils.String(v.(string))
	}

	if v, ok := d.GetOk("source_resource_id"); ok {
		properties.SnapshotProperties.CreationData.SourceResourceID = utils.String(v.(string))
	}

	if v, ok := d.GetOk("storage_account_id"); ok {
		properties.SnapshotProperties.CreationData.StorageAccountID = utils.String(v.(string))
	}

	diskSizeGB := d.Get("disk_size_gb").(int)
	if diskSizeGB > 0 {
		properties.SnapshotProperties.DiskSizeGB = utils.Int32(int32(diskSizeGB))
	}

	if v, ok := d.GetOk("encryption_settings"); ok {
		encryptionSettings := v.([]interface{})
		settings := encryptionSettings[0].(map[string]interface{})
		properties.EncryptionSettings = expandManagedDiskEncryptionSettings(settings)
	}

	future, err := client.CreateOrUpdate(ctx, resourceGroup, name, properties)
	if err != nil {
		return fmt.Errorf("Error issuing create/update request for Snapshot %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("Error waiting on create/update future for Snapshot %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	resp, err := client.Get(ctx, resourceGroup, name)
	if err != nil {
		return fmt.Errorf("Error issuing get request for Snapshot %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	d.SetId(*resp.ID)

	return resourceArmSnapshotRead(d, meta)
}

func resourceArmSnapshotRead(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).compute.SnapshotsClient
	ctx := meta.(*ArmClient).StopContext

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup := id.ResourceGroup
	name := id.Path["snapshots"]

	resp, err := client.Get(ctx, resourceGroup, name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[INFO] Error reading Snapshot %q - removing from state", d.Id())
			d.SetId("")
			return nil
		}

		return fmt.Errorf("Error making Read request on Snapshot %q: %+v", name, err)
	}

	d.Set("name", resp.Name)
	d.Set("resource_group_name", resourceGroup)
	if location := resp.Location; location != nil {
		d.Set("location", azure.NormalizeLocation(*location))
	}

	if props := resp.SnapshotProperties; props != nil {

		if data := props.CreationData; data != nil {
			d.Set("create_option", string(data.CreateOption))

			if accountId := data.StorageAccountID; accountId != nil {
				d.Set("storage_account_id", accountId)
			}
		}

		if props.DiskSizeGB != nil {
			d.Set("disk_size_gb", int(*props.DiskSizeGB))
		}

		if props.EncryptionSettings != nil {
			d.Set("encryption_settings", flattenManagedDiskEncryptionSettings(props.EncryptionSettings))
		}
	}

	flattenAndSetTags(d, resp.Tags)

	return nil
}

func resourceArmSnapshotDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*ArmClient).compute.SnapshotsClient
	ctx := meta.(*ArmClient).StopContext

	id, err := parseAzureResourceID(d.Id())
	if err != nil {
		return err
	}

	resourceGroup := id.ResourceGroup
	name := id.Path["snapshots"]

	future, err := client.Delete(ctx, resourceGroup, name)
	if err != nil {
		return fmt.Errorf("Error deleting Snapshot: %+v", err)
	}

	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("Error deleting Snapshot: %+v", err)
	}

	return nil
}

func validateSnapshotName(v interface{}, _ string) (warnings []string, errors []error) {
	// a-z, A-Z, 0-9, _ and -. The max name length is 80
	value := v.(string)

	r, _ := regexp.Compile("^[A-Za-z0-9_-]+$")
	if !r.MatchString(value) {
		errors = append(errors, fmt.Errorf("Snapshot Names can only contain alphanumeric characters and underscores."))
	}

	length := len(value)
	if length > 80 {
		errors = append(errors, fmt.Errorf("Snapshot Name can be up to 80 characters, currently %d.", length))
	}

	return warnings, errors
}
