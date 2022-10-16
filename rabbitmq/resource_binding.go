package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/customdiff"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/structure"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	rabbithole "github.com/michaelklishin/rabbit-hole/v2"
)

func resourceBinding() *schema.Resource {
	return &schema.Resource{
		Create: CreateBinding,
		Read:   ReadBinding,
		Delete: DeleteBinding,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		CustomizeDiff: customdiff.All(customdiff.IfValueChange("source",
			func(_ context.Context, old, new, _ interface{}) bool { return old != new },
			func(_ context.Context, diff *schema.ResourceDiff, _ interface{}) error {

				bindingId := strings.Split(diff.Id(), "/")

				log.Printf("[DEBUG] seven id: %s", diff.Id())

				_old, _new := diff.GetChange("source")

				log.Printf("[DEBUG] old source: %s", _old)
				log.Printf("[DEBUG] new source: %s", _new)

				if len(bindingId) >= 6 {

					if strings.Contains(_new.(string), bindingId[5]) {

						log.Printf("[DEBUG] Use old values for id: %s, old: %s, new: %s", diff.Id(), _old, _new)

						return diff.SetNew("source", _old)
					}
				}

				return nil
			},
		),
			customdiff.IfValueChange("destination",
				func(_ context.Context, old, new, _ interface{}) bool { return old != new },
				func(_ context.Context, diff *schema.ResourceDiff, _ interface{}) error {

					bindingId := strings.Split(diff.Id(), "/")

					log.Printf("[DEBUG] seven id: %s", diff.Id())

					_old, _new := diff.GetChange("destination")

					log.Printf("[DEBUG] old destination: %s", _old)
					log.Printf("[DEBUG] new destination: %s", _new)

					if len(bindingId) == 7 {

						if strings.Contains(_new.(string), bindingId[6]) {

							log.Printf("[DEBUG] Use old values for id: %s, old: %s, new: %s", diff.Id(), _old, _new)

							return diff.SetNew("destination", _old)
						}
					}

					return nil
				},
			),
		),
		Schema: map[string]*schema.Schema{
			"source": {
				Type:     schema.TypeString,
				Required: false,
				Optional: true,
				//ForceNew: true,
				Computed: true,
			},

			"vhost": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"destination": {
				Type:     schema.TypeString,
				Required: false,
				Optional: true,
				//ForceNew: true,
				Computed: true,
			},

			"destination_type": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"properties_key": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"routing_key": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},

			"arguments": {
				Type:          schema.TypeMap,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"arguments_json"},
			},
			"arguments_json": {
				Type:             schema.TypeString,
				Optional:         true,
				ForceNew:         true,
				ValidateFunc:     validation.StringIsJSON,
				ConflictsWith:    []string{"arguments"},
				DiffSuppressFunc: structure.SuppressJsonDiff,
			},
		},
	}
}

func CreateBinding(d *schema.ResourceData, meta interface{}) error {
	rmqc := meta.(*rabbithole.Client)

	vhost := d.Get("vhost").(string)
	arguments := d.Get("arguments").(map[string]interface{})

	// If arguments_json is used, unmarshal it into a generic interface
	// and use it as the "arguments" key for the binding.
	if v, ok := d.Get("arguments_json").(string); ok && v != "" {
		var arguments_json map[string]interface{}
		err := json.Unmarshal([]byte(v), &arguments_json)
		if err != nil {
			return err
		}

		arguments = arguments_json
	}

	srcName, _, guidSrc := parseStandardIdWithFallback(d.Get("source").(string))
	dstName, _, guidDst := parseStandardIdWithFallback(d.Get("destination").(string))

	bindingInfo := rabbithole.BindingInfo{

		Source:      srcName,
		Destination: dstName,

		DestinationType: d.Get("destination_type").(string),
		RoutingKey:      d.Get("routing_key").(string),

		Arguments: arguments,
	}

	propertiesKey, err := declareBinding(rmqc, vhost, bindingInfo)

	if err != nil {

		return err
	}

	log.Printf("[DEBUG] RabbitMQ: Binding properties key: %s", propertiesKey)

	bindingInfo.PropertiesKey = propertiesKey

	/*
		Use the guid parsed from the identifiers of the parent resources:
		queue or exchange. Since the guid changes every time the resource
		is recreated, it will help to track changes at the level of child
		resources and recreate them.

		https://github.com/cyrilgdn/terraform-provider-rabbitmq/issues/34
		https://github.com/cyrilgdn/terraform-provider-rabbitmq/issues/25
	*/

	d.SetId(strings.TrimRight(fmt.Sprintf("%s/%s/%s/%s/%s/%s/%s", percentEncodeSlashes(vhost), bindingInfo.Source, bindingInfo.Destination, bindingInfo.DestinationType, bindingInfo.PropertiesKey, guidSrc, guidDst), "/"))

	return ReadBinding(d, meta)
}

func ReadBinding(d *schema.ResourceData, meta interface{}) error {
	rmqc := meta.(*rabbithole.Client)

	log.Printf("[TRACE] RabbitMQ: read binding resource ID (pre-split): %s", d.Id())
	bindingId := strings.Split(d.Id(), "/")
	log.Printf("[DEBUG] RabbitMQ: binding ID: %#v", bindingId)
	if len(bindingId) < 5 {
		return fmt.Errorf("Unable to determine binding ID")
	}

	vhost := percentDecodeSlashes(bindingId[0])
	source := bindingId[1]
	destination := bindingId[2]

	destinationType := bindingId[3]
	propertiesKey := bindingId[4]
	log.Printf("[DEBUG] RabbitMQ: Attempting to find binding for: vhost=%s source=%s destination=%s destinationType=%s propertiesKey=%s",
		vhost, source, destination, destinationType, propertiesKey)

	bindings, err := rmqc.ListBindingsIn(vhost)
	if err != nil {
		return err
	}

	log.Printf("[DEBUG] RabbitMQ: Bindings retrieved: %#v", bindings)
	bindingFound := false
	for _, binding := range bindings {
		log.Printf("[TRACE] RabbitMQ: Assessing binding: %#v", binding)

		if binding.Source == source && binding.Destination == destination && binding.DestinationType == destinationType && binding.PropertiesKey == propertiesKey {
			log.Printf("[DEBUG] RabbitMQ: Found Binding: %#v", binding)
			bindingFound = true

			d.Set("vhost", binding.Vhost)
			d.Set("source", source)
			d.Set("destination", destination)

			d.Set("destination_type", binding.DestinationType)
			d.Set("routing_key", binding.RoutingKey)
			d.Set("properties_key", binding.PropertiesKey)

			if v, ok := d.Get("arguments_json").(string); ok && v != "" {
				bytes, err := json.Marshal(binding.Arguments)
				if err != nil {
					return fmt.Errorf("could not encode arguments as JSON: %w", err)
				}
				d.Set("arguments_json", string(bytes))
			} else {
				d.Set("arguments", binding.Arguments)
			}
		}
	}

	// The binding could not be found,
	// so consider it deleted and remove from state
	if !bindingFound {
		d.SetId("")
	}

	return nil
}

func DeleteBinding(d *schema.ResourceData, meta interface{}) error {
	rmqc := meta.(*rabbithole.Client)

	bindingId := strings.Split(d.Id(), "/")
	if len(bindingId) < 5 {
		return fmt.Errorf("Unable to determine binding ID")
	}

	vhost := percentDecodeSlashes(bindingId[0])
	source := bindingId[1]
	destination := bindingId[2]
	destinationType := bindingId[3]
	propertiesKey := bindingId[4]

	bindingInfo := rabbithole.BindingInfo{
		Vhost:           vhost,
		Source:          source,
		Destination:     destination,
		DestinationType: destinationType,
		PropertiesKey:   propertiesKey,
	}

	log.Printf("[DEBUG] RabbitMQ: Attempting to delete binding for: vhost=%s source=%s destination=%s destinationType=%s propertiesKey=%s",
		vhost, source, destination, destinationType, propertiesKey)

	resp, err := rmqc.DeleteBinding(vhost, bindingInfo)
	if err != nil {
		return err
	}

	log.Printf("[DEBUG] RabbitMQ: Binding delete response: %#v", resp)

	if resp.StatusCode == 404 {
		// The binding was already deleted
		return nil
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("Error deleting RabbitMQ binding: %s", resp.Status)
	}

	return nil
}

func declareBinding(rmqc *rabbithole.Client, vhost string, bindingInfo rabbithole.BindingInfo) (string, error) {
	log.Printf("[DEBUG] RabbitMQ: Attempting to declare binding for: vhost=%s source=%s destination=%s destinationType=%s",
		vhost, bindingInfo.Source, bindingInfo.Destination, bindingInfo.DestinationType)

	resp, err := rmqc.DeclareBinding(vhost, bindingInfo)
	log.Printf("[DEBUG] RabbitMQ: Binding declare response: %#v", resp)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("Error declaring RabbitMQ binding: %s", resp.Status)
	}

	location := strings.Split(resp.Header.Get("Location"), "/")
	propertiesKey, err := url.PathUnescape(location[len(location)-1])

	if err != nil {
		return "", err
	}

	return propertiesKey, nil
}
