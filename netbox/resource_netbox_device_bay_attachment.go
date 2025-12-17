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

		Description: `:meta:subcategory:Data Center Infrastructure Management (DCIM):Installs a device into an existing device bay. This resource is useful when device bays are created automatically by other resource and you want to attach a device to the bay.

**Note:** This resource only manages the installation/attachment of a device to a bay. It does not create or delete the bay itself. Use ` + "`netbox_device_bay`" + ` to create and manage bays.

**Import:** This resource can be imported using the composite ID format ` + "`device_id:bay_name`" + ` (e.g., ` + "`123:BAY1`" + `). For backward compatibility, importing with just the bay ID is also supported, but the ID will be converted to the composite format.`,

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
				Description: "Name of the device bay. The bay must already exist on the parent device.",
			},
			"installed_device_id": {
				Type:        schema.TypeInt,
				Required:    true,
				Description: "ID of the device to install into the bay. Child device of the bay.",
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
	}
}

// findDeviceBayByDeviceAndName searches for an existing device bay by device_id(parent device) and name(bay name).
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
		return 0, nil 
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

	//Get bay ID
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}

	if bayID == 0 {
		return diag.Errorf("device bay %q not found on device %d. The bay must exist before it can be used for attachment. Bays can be created by netbox_device_bay resource.", bayName, deviceID)
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
	// Preserve all existing properties (name, label, description, tags, custom fields)
	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: &installedDeviceID,
		Description:     currentBay.Description,
		Tags:            currentBay.Tags,            
		CustomFields:    currentBay.CustomFields,  
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

	id := d.Id()
	var deviceID int64
	var bayName string
	var bay *models.DeviceBay

	// Parse ID: composite format "device_id:bay_name" or legacy format (just bay ID)
	if parts := strings.Split(id, ":"); len(parts) == 2 {
		// Composite ID format: parse device_id and bay_name
		var err error
		deviceID, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
		}
		bayName = parts[1]

		// Find and read the bay using device_id and bay_name
		bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
		if err != nil {
			return diag.FromErr(err)
		}
		if bayID == 0 {
			d.SetId("")
			return nil
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
		bay = res.GetPayload()

	} else {
		// Legacy format, convert bay ID to composite format
		bayID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return diag.FromErr(fmt.Errorf("invalid ID format: expected 'device_id:bay_name' or bay ID, got %q", id))
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

		bay = res.GetPayload()
		if bay.Device == nil {
			return diag.Errorf("device bay has no associated device")
		}

		// Verify that a device is installed before importing
		if bay.InstalledDevice == nil {
			return diag.Errorf("cannot import device_bay_attachment: bay %d has no device installed. The bay must have a device installed to import as an attachment resource.", bayID)
		}

		deviceID = bay.Device.ID
		bayName = *bay.Name

		// Update ID to composite format for future reads
		d.SetId(fmt.Sprintf("%d:%s", deviceID, bayName))
	}

	// Verify that a device is still installed in the bay
	// This handles the case where the device was uninstalled outside of Terraform
	if bay.InstalledDevice == nil {
		d.SetId("")
		return nil
	}

	// Update Terraform state with current values
	d.Set("device_id", deviceID)
	d.Set("bay_name", bayName)
	d.Set("installed_device_id", bay.InstalledDevice.ID)

	return nil
}

func resourceNetboxDeviceBayAttachmentUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	api := m.(*providerState)

	// Note: Update is only called when installed_device_id changes.
	// device_id and bay_name are ForceNew: true, so changing them would cause Terraform to destroy and recreate the resource instead of updating.

	// Parse ID (always in composite format at this point)
	id := d.Id()
	parts := strings.Split(id, ":")
	if len(parts) != 2 {
		return diag.Errorf("invalid ID format: expected 'device_id:bay_name'")
	}

	deviceID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
	}
	bayName := parts[1]

	// Find the bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}
	if bayID == 0 {
		return diag.Errorf("device bay %q not found on device %d", bayName, deviceID)
	}

	// Read current bay state to preserve all properties
	readParams := dcim.NewDcimDeviceBaysReadParams().WithID(bayID)
	readRes, err := api.Dcim.DcimDeviceBaysRead(readParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error reading device bay: %w", err))
	}

	currentBay := readRes.GetPayload()

	// Get the new installed device ID (only field that can change)
	newInstalledDeviceID := int64(d.Get("installed_device_id").(int))

	// Update bay with new installed device, preserving all other properties
	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: &newInstalledDeviceID,
		Description:     currentBay.Description,
		Tags:            currentBay.Tags,
		CustomFields:    currentBay.CustomFields,
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

	// Parse ID (always in composite format at this point, but validate for safety)
	id := d.Id()
	parts := strings.Split(id, ":")
	if len(parts) != 2 {
		return diag.Errorf("invalid ID format: expected 'device_id:bay_name'")
	}

	deviceID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return diag.FromErr(fmt.Errorf("invalid device_id in ID: %w", err))
	}
	bayName := parts[1]

	// Find the bay
	bayID, err := findDeviceBayByDeviceAndName(api, deviceID, bayName)
	if err != nil {
		return diag.FromErr(err)
	}
	if bayID == 0 {
		// Bay doesn't exist anymore, nothing to do
		return nil
	}

	// Read current bay state to preserve all properties
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

	// Remove the attachment by setting InstalledDevice to nil, preserving all other properties
	data := models.WritableDeviceBay{
		Device:          int64ToPtr(deviceID),
		Name:            currentBay.Name,
		Label:           currentBay.Label,
		InstalledDevice: nil, // Remove installation
		Description:     currentBay.Description,
		Tags:            currentBay.Tags,
		CustomFields:    currentBay.CustomFields,
	}

	updateParams := dcim.NewDcimDeviceBaysPartialUpdateParams().WithID(bayID).WithData(&data)
	_, err = api.Dcim.DcimDeviceBaysPartialUpdate(updateParams, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error uninstalling device from bay: %w", err))
	}

	// Note: Terraform automatically removes the resource from state after Delete returns successfully
	return nil
}

