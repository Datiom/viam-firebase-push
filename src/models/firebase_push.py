import json
from typing import ClassVar, Final, Mapping, Optional, Sequence, Dict, Any, List
import base64
import uuid
import io
from datetime import datetime, timedelta

from typing_extensions import Self
from viam.proto.app.robot import ComponentConfig
from viam.proto.common import ResourceName
from viam.resource.base import ResourceBase
from viam.resource.easy_resource import EasyResource
from viam.resource.types import Model, ModelFamily
from viam.services.generic import *
from viam.utils import ValueTypes, struct_to_dict
from viam.logging import getLogger

try:
    import firebase_admin
    from firebase_admin import credentials, messaging, storage
except ImportError:
    raise ImportError("firebase_admin package is required. Install with 'pip install firebase-admin'")


class FirebasePush(Generic, EasyResource):
    MODEL: ClassVar[Model] = Model(
        ModelFamily("datiom", "service"), "firebase-push"
    )

    # Attributes
    firebase_app = None
    storage_bucket = None
    fcm_tokens: List[str] = []
    preset_messages: Dict[str, Dict[str, Any]] = {}
    enforce_preset: bool = False
    
    @classmethod
    def new(
        cls, config: ComponentConfig, dependencies: Mapping[ResourceName, ResourceBase]
    ) -> Self:
        service = cls(config.name)
        service.reconfigure(config, dependencies)
        return service

    @classmethod
    def validate_config(cls, config: ComponentConfig) -> Sequence[str]:
        attributes = struct_to_dict(config.attributes)
        
        # Check that either service_account_json or service_account_file is provided
        if not (attributes.get("service_account_json") or attributes.get("service_account_file")):
            raise Exception("Either service_account_json or service_account_file must be provided")
            
        # FCM tokens are now optional as they can be passed via doCommand
        return []

    def reconfigure(
        self, config: ComponentConfig, dependencies: Mapping[ResourceName, ResourceBase]
    ):
        attributes = struct_to_dict(config.attributes)
        
        # Initialize Firebase app with credentials
        if self.firebase_app:
            # Clean up existing app
            firebase_admin.delete_app(self.firebase_app)
            self.firebase_app = None
            self.storage_bucket = None
            
        # Get credentials
        cred = None
        service_account_json = attributes.get("service_account_json")
        service_account_file = attributes.get("service_account_file")
        
        if service_account_json:
            # If it's a string, parse it as JSON
            if isinstance(service_account_json, str):
                try:
                    service_account_json = json.loads(service_account_json)
                except json.JSONDecodeError:
                    raise Exception("Invalid service_account_json: not a valid JSON string")
                    
            cred = credentials.Certificate(service_account_json)
        elif service_account_file:
            cred = credentials.Certificate(service_account_file)
        else:
            raise Exception("Either service_account_json or service_account_file must be provided")
            
        # Initialize Firebase app
        app_options = {}
        storage_bucket_name = attributes.get("storage_bucket_name")
        if storage_bucket_name:
            app_options['storageBucket'] = storage_bucket_name
            
        self.firebase_app = firebase_admin.initialize_app(cred, options=app_options)
        # Get a reference to the default storage bucket
        try:
            self.storage_bucket = storage.bucket(app=self.firebase_app)
            self.logger.info(f"Successfully connected to Firebase Storage bucket: {self.storage_bucket.name}")
        except Exception as e:
            self.logger.error(f"Failed to connect to Firebase Storage bucket: {e}. Image upload via media_base64 will not work.")
            self.storage_bucket = None
        
        # Get FCM tokens (now optional)
        self.fcm_tokens = []
        fcm_tokens_value = attributes.get("fcm_tokens")
        if fcm_tokens_value:
            if isinstance(fcm_tokens_value, str):
                try:
                    self.fcm_tokens = json.loads(fcm_tokens_value)
                    if not isinstance(self.fcm_tokens, list):
                        self.fcm_tokens = [self.fcm_tokens]
                except json.JSONDecodeError:
                    self.fcm_tokens = [fcm_tokens_value]
            else:
                # If it's already a list or other type, use it directly
                if not isinstance(fcm_tokens_value, list):
                    self.fcm_tokens = [fcm_tokens_value]
                else:
                    self.fcm_tokens = fcm_tokens_value
        
        # Get preset messages
        preset_messages_value = attributes.get("preset_messages")
        if preset_messages_value:
            if isinstance(preset_messages_value, str):
                self.preset_messages = json.loads(preset_messages_value)
            else:
                self.preset_messages = preset_messages_value
        else:
            self.preset_messages = {}
            
        # Check if we should enforce using preset messages
        enforce_preset_value = attributes.get("enforce_preset", False)
        self.enforce_preset = bool(enforce_preset_value)
            
        # Log successful configuration
        if self.fcm_tokens:
            self.logger.info(f"Firebase Push configured with {len(self.fcm_tokens)} default FCM tokens")
        else:
            self.logger.info("Firebase Push configured with no default FCM tokens")
            
        if self.preset_messages:
            self.logger.info(f"Configured with {len(self.preset_messages)} preset messages")

    async def do_command(
        self,
        command: Mapping[str, ValueTypes],
        *,
        timeout: Optional[float] = None,
        **kwargs
    ) -> Mapping[str, ValueTypes]:
        """Execute commands for the Firebase Push service"""
        if not command or "command" not in command:
            raise Exception("Missing 'command' field in request")
            
        cmd = command["command"]
        
        if cmd == "send":
            return await self._handle_send(command)
        else:
            raise Exception(f"Unknown command: {cmd}")
            
    async def _handle_send(self, command: Mapping[str, ValueTypes]) -> Mapping[str, ValueTypes]:
        """Handle the send command to send push notifications"""
        # Check if we need to use presets
        if self.enforce_preset and (not command.get("preset") or not self.preset_messages):
            raise Exception("Service is configured to enforce presets, but no preset was specified or no presets are configured")
            
        # Get notification content
        preset_name = command.get("preset")
        template_vars = command.get("template_vars", {})
        
        image_url_from_command = command.get("image_url")
        media_base64 = command.get("media_base64")
        media_mime_type = command.get("media_mime_type")
        
        processed_image_url = None

        # If preset is specified, use it
        if preset_name:
            if preset_name not in self.preset_messages:
                raise Exception(f"Preset '{preset_name}' not found in configured presets")
                
            preset = self.preset_messages[preset_name]
            title = preset.get("title", "")
            body = preset.get("body", "")
            preset_image_url = preset.get("image_url")
            
            # Apply template variables
            if template_vars:
                for key, value in template_vars.items():
                    placeholder = f"<<{key}>>"
                    title = title.replace(placeholder, str(value))
                    body = body.replace(placeholder, str(value))
                    if preset_image_url:
                        preset_image_url = preset_image_url.replace(placeholder, str(value))
            
            if preset_image_url:
                processed_image_url = preset_image_url

        else:
            # Use direct parameters
            title = command.get("title", "")
            body = command.get("body", "")
            if image_url_from_command:
                 processed_image_url = image_url_from_command

        # Handle base64 image upload if provided, this overrides any other image_url
        blob_to_delete = None
        if media_base64 and media_mime_type:
            if not self.storage_bucket:
                self.logger.warning("media_base64 provided, but storage bucket is not configured/initialized. Cannot upload image.")
            else:
                try:
                    image_bytes = base64.b64decode(media_base64)
                    filename = f"push_image_{uuid.uuid4()}"
                    
                    blob = self.storage_bucket.blob(filename)
                    
                    # Use io.BytesIO for upload_from_file which is more general
                    image_stream = io.BytesIO(image_bytes)
                    blob.upload_from_file(image_stream, content_type=media_mime_type)
                    image_stream.close()
                    
                    # Generate signed URL (e.g., valid for 15 minutes)
                    processed_image_url = blob.generate_signed_url(
                        expiration=timedelta(minutes=15),
                        method='GET'
                    )
                    blob_to_delete = blob
                    self.logger.info(f"Uploaded base64 image to {filename}, signed URL: {processed_image_url}")
                except Exception as e:
                    self.logger.error(f"Error processing media_base64: {e}. Proceeding without image from base64.")
                    # Keep previously set processed_image_url (from command or preset) if base64 fails

        # Get data payload
        data = command.get("data", {})
        
        # Validate required fields
        if not title and not body:
            raise Exception("Either title or body must be provided")
            
        # Get FCM tokens - prioritize tokens passed in command over configured tokens
        target_tokens = []
        cmd_tokens = command.get("fcm_tokens", [])
        
        # Process command tokens if provided
        if cmd_tokens:
            # Handle various formats
            if isinstance(cmd_tokens, str):
                try:
                    parsed_tokens = json.loads(cmd_tokens)
                    if isinstance(parsed_tokens, list):
                        target_tokens = parsed_tokens
                    else:
                        target_tokens = [parsed_tokens]
                except json.JSONDecodeError:
                    # Not a JSON string, treat as a single token
                    target_tokens = [cmd_tokens]
            elif isinstance(cmd_tokens, list):
                target_tokens = cmd_tokens
            else:
                target_tokens = [cmd_tokens]
        
        # If no tokens provided in command, use configured tokens
        if not target_tokens:
            target_tokens = self.fcm_tokens
            
        # Check that we have at least one token
        if not target_tokens:
            raise Exception("No FCM tokens provided. Either configure tokens or provide them in the command.")
            
        # Send notification to all target tokens
        failed_tokens = []
        successful_count = 0
        
        for token in target_tokens:
            try:
                # Construct the basic message
                message_args = {
                    "data": data,
                    "token": token,
                }

                # Add notification part if title or body is present
                if title or body:
                     message_args["notification"] = messaging.Notification(title=title, body=body)

                # Add image if processed_image_url is available
                if processed_image_url:
                    if "notification" not in message_args or message_args["notification"] is None:
                         # if only image is sent, we need a notification object
                         message_args["notification"] = messaging.Notification(image=processed_image_url)
                    else:
                        message_args["notification"].image = processed_image_url

                    # Platform-specific configurations for image display
                    message_args["android"] = messaging.AndroidConfig(
                        notification=messaging.AndroidNotification(image=processed_image_url)
                    )
                    message_args["apns"] = messaging.APNSConfig(
                        payload=messaging.APNSPayload(
                            aps=messaging.Aps(mutable_content=True)
                        ),
                        fcm_options=messaging.APNSFCMOptions(image=processed_image_url)
                    )
                    message_args["webpush"] = messaging.WebpushConfig(
                        notification=messaging.WebpushNotification(image=processed_image_url)
                    )

                # Ensure there's at least a notification or data part
                if "notification" not in message_args and not data:
                    # This case should ideally be caught earlier (e.g. title/body/data required)
                    # but as a safeguard for message structure:
                    if processed_image_url: # If only image was given, it's in notification now
                         pass # Notification object was created for the image
                    else:
                        self.logger.warning(f"Skipping token {token} as there is no title, body, data, or image to send.")
                        failed_tokens.append(token)
                        continue

                message = messaging.Message(**message_args)
                
                response = messaging.send(message, app=self.firebase_app)
                successful_count += 1
                self.logger.debug(f"Successfully sent message: {response}")
            except Exception as e:
                self.logger.error(f"Error sending notification to token {token}: {e}")
                failed_tokens.append(token)
                
        result = {
            "success": successful_count > 0,
            "sent_count": successful_count,
            "failed_count": len(failed_tokens),
            "sent_to_tokens": successful_count
        }
        
        # Delete uploaded image from storage if it exists
        if blob_to_delete:
            try:
                blob_to_delete.delete()
                self.logger.info(f"Successfully deleted temporary image {blob_to_delete.name} from Firebase Storage.")
            except Exception as e:
                self.logger.error(f"Failed to delete temporary image {blob_to_delete.name} from Firebase Storage: {e}")
        
        return result

