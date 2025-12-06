package netbox

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fbreckle/go-netbox/netbox/client/dcim"
	"github.com/fbreckle/go-netbox/netbox/models"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceNetboxDeviceBayAttachment() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceNetboxDeviceBayAttachmentCreate,
		ReadContext:   resourceNetboxDeviceBayAttachmentRead,
		UpdateContext: resourceNetboxDeviceBayAttachmentUpdate,
		DeleteContext: resourceNetboxDeviceBayAttachmentDelete,

		Description: `:meta:subcategory:Data Center Infrastructure Management (DCIM):Installs a device into an existing device bay. This resource is useful when device bays are created automatically from device_type templates (e.g., panels with pre-defined bays).

**Note:** This resource only manages the installation/attachment of a device to a bay. It does not create or delete the bay itself. Use ` + "`netbox_device_bay`" + ` to create and manage bays.`,

		Schema: map[string]*schema.Schema{
			"device_id": {
				Type:        schema.TypeInt,
				Required:    true,
				ForceNew:    true,
				Description: "ID of the device that contains the bay. Parent device of the bay.",
			},
			"bay_name": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Name of the device bay (e.g., \"BKC1\", \"C1\"). The bay must already exist on the device.",
			},
			"installed_device_id": {
				Type:        schema.TypeInt,
				Required:    true,
				Description: "ID of the device to install into the bay.",
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
	}
}

// findDeviceBayByDeviceAndName searches for an existing device bay by device_id and name.
// Returns the bay ID if found, or 0 if not found.
func findDeviceBayByDeviceAndName(api *providerState, deviceID int64, name string) (int64, error) {
	params := dcim.NewDcimDeviceBaysListParams()
	deviceIDStr := strconv.FormatInt(deviceID, 10)
	params.DeviceID = &deviceIDStr
	params.Name = &name
	limit := int64(2) 
	params.Limit = &limit

	res, err := api.Dcim.DcimDeviceBaysList(params, nil)
	if err != nil {
		return 0, fmt.Errorf("error searching for device bay: %w", err)
	}

	payload := res.GetPayload()
	if payload.Count == nil || *payload.Count == 0 {
		return 0, nil // Not found
	}

	if *payload.Count > 1 {
		return 0, fmt.Errorf("multiple device bays found with name %q on device %d", name, deviceID)
	}

	return payload.Results[0].ID, nil
}

func resourceNetboxDeviceBayAttachmentCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	api := m.(*providerState)

	deviceID := int64(d.Get("device_id").(int))
	bayName := d.Get("bay_name").(string)
	installedDeviceID := int64(d.Get("installed_device_id").(int))

	// Find the existing bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}

	if bayID == 0 {
		return diag.Errorf("device bay %q not found on device %d. The bay must exist before it can be used for attachment. Bays are typically created automatically from device_type templates.", bayName, deviceID)
	}

	// Read the current bay to get all its properties
	readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
	readRes, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading device bay: %w", err))
	}

	currentBay := readRes.GetPayload()

	// Check if bay already has a device installed
	if currentBay.InstalledDevice != nil {
		return diag.Errorf("device bay %q on device %d already has device %d installed. Remove the existing installation first.", bayName, deviceID, currentBay.InstalledDevice.ID)
	}

	// Update the bay to install the device
	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: &installedDeviceID,
		Description:     currentBay.Description,
	}

	// Preserve existing tags and custom fields
	if currentBay.Tags != nil {
		data.Tags = currentBay.Tags
	}
	if currentBay.CustomFields != nil {
		data.CustomFields = currentBay.CustomFields
	}

	updateParams := dcim.NewDcimDeviceBaysPartialUpdateParams().WithID(bayID).WithData(&data)
	_, err = api.Dcim.DcimDeviceBaysPartialUpdate(updateParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error installing device into bay: %w", err))
	}

	// Use a composite ID: device_id:bay_name
	d.SetId(fmt.Sprintf("%d:%s", deviceID, bayName))

	return resourceNetboxDeviceBayAttachmentRead(ctx, d, m)
}

func resourceNetboxDeviceBayAttachmentRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	api := m.(*providerState)

	// Parse the composite ID: device_id:bay_name
	id := d.Id()
	var deviceID int64
	var bayName string

	// Try to parse as composite ID first
	if parts := strings.Split(id, ":"); len(parts) == 2 {
		var err error
		deviceID, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
		}
		bayName = parts[1]
	} else {
		// Fallback: assume it's just the bay ID (for backward compatibility with import)
		bayID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid ID format: %w", err))
		}

		readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
		res, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
		if err != nil {
			if errresp, ok := err.(*dcim.DcimDeviceBaysReadDefault); ok {
				if errresp.Code() == 404 {
					d.SetId("")
					return nil
				}
			}
			return diag.FromErr(err)
		}

		bay := res.GetPayload()
		if bay.Device == nil {
			return diag.Errorf("device bay has no associated device")
		}
		deviceID = bay.Device.ID
		bayName = *bay.Name

		// Update ID to composite format
		d.SetId(fmt.Sprintf("%d:%s", deviceID, bayName))
	}

	// Find the bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}

	if bayID == 0 {
		d.SetId("")
		return nil
	}

	// Read the bay
	readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
	res, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
	if err != nil {
		if errresp, ok := err.(*dcim.DcimDeviceBaysReadDefault); ok {
			if errresp.Code() == 404 {
				d.SetId("")
				return nil
			}
		}
		return diag.FromErr(err)
	}

	bay := res.GetPayload()

	// Check if device is still installed
	if bay.InstalledDevice == nil {
		// Device was uninstalled outside of Terraform
		d.SetId("")
		return nil
	}

	d.Set("device_id", deviceID)
	d.Set("bay_name", bayName)
	d.Set("installed_device_id", bay.InstalledDevice.ID)

	return nil
}

func resourceNetboxDeviceBayAttachmentUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	api := m.(*providerState)

	// Parse ID
	id := d.Id()
	var deviceID int64
	var bayName string

	if parts := strings.Split(id, ":"); len(parts) == 2 {
		var err error
		deviceID, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
		}
		bayName = parts[1]
	} else {
		return diag.Errorf("invalid ID format")
	}

	// Find the bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}

	if bayID == 0 {
		return diag.Errorf("device bay %q not found on device %d", bayName, deviceID)
	}

	// Read current bay state
	readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
	readRes, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading device bay: %w", err))
	}

	currentBay := readRes.GetPayload()

	// Update with new installed device
	newInstalledDeviceID := int64(d.Get("installed_device_id").(int))

	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: &newInstalledDeviceID,
		Description:     currentBay.Description,
	}

	// Preserve existing tags and custom fields
	if currentBay.Tags != nil {
		data.Tags = currentBay.Tags
	}
	if currentBay.CustomFields != nil {
		data.CustomFields = currentBay.CustomFields
	}

	updateParams := dcim.NewDcimDeviceBaysPartialUpdateParams().WithID(bayID).WithData(&data)
	_, err = api.Dcim.DcimDeviceBaysPartialUpdate(updateParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error updating device bay attachment: %w", err))
	}

	return resourceNetboxDeviceBayAttachmentRead(ctx, d, m)
}

func resourceNetboxDeviceBayAttachmentDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	api := m.(*providerState)

	// Parse ID
	id := d.Id()
	var deviceID int64
	var bayName string

	if parts := strings.Split(id, ":"); len(parts) == 2 {
		var err error
		deviceID, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
		}
		bayName = parts[1]
	} else {
		return diag.Errorf("invalid ID format")
	}

	// Find the bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}

	if bayID == 0 {
		// Bay doesn't exist anymore, nothing to do
		return nil
	}

	// Read current bay state
	readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
	readRes, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
	if err != nil {
		if errresp, ok := err.(*dcim.DcimDeviceBaysReadDefault); ok {
			if errresp.Code() == 404 {
				// Bay doesn't exist, nothing to do
				return nil
			}
		}
		return diag.FromErr(fmt.Errorf("error reading device bay: %w", err))
	}

	currentBay := readRes.GetPayload()

	// Only remove the attachment
	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: nil, // Remove installation
		Description:     currentBay.Description,
	}


	if currentBay.Tags != nil {
		data.Tags = currentBay.Tags
	}
	if currentBay.CustomFields != nil {
		data.CustomFields = currentBay.CustomFields
	}

	updateParams := dcim.NewDcimDeviceBaysPartialUpdateParams().WithID(bayID).WithData(&data)
	_, err = api.Dcim.DcimDeviceBaysPartialUpdate(updateParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error uninstalling device from bay: %w", err))
	}

	return nil
}

