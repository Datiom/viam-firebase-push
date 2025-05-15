import json
from typing import ClassVar, Final, Mapping, Optional, Sequence, Dict, Any, List

from typing_extensions import Self
from viam.proto.app.robot import ComponentConfig
from viam.proto.common import ResourceName
from viam.resource.base import ResourceBase
from viam.resource.easy_resource import EasyResource
from viam.resource.types import Model, ModelFamily
from viam.services.generic import *
from viam.utils import ValueTypes
from viam.logging import getLogger

try:
    import firebase_admin
    from firebase_admin import credentials, messaging
except ImportError:
    raise ImportError("firebase_admin package is required. Install with 'pip install firebase-admin'")


class FirebasePush(Generic, EasyResource):
    MODEL: ClassVar[Model] = Model(
        ModelFamily("datiom", "service"), "firebase-push"
    )

    # Attributes
    firebase_app = None
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
        attributes = config.attributes.fields
        
        # Check that either service_account_json or service_account_file is provided
        if not (attributes.get("service_account_json") or attributes.get("service_account_file")):
            raise Exception("Either service_account_json or service_account_file must be provided")
            
        # Check that fcm_tokens is provided and is a list
        if not attributes.get("fcm_tokens"):
            raise Exception("fcm_tokens must be provided in the attributes")
            
        return []

    def reconfigure(
        self, config: ComponentConfig, dependencies: Mapping[ResourceName, ResourceBase]
    ):
        attributes = config.attributes.fields
        
        # Initialize Firebase app with credentials
        if self.firebase_app:
            # Clean up existing app
            firebase_admin.delete_app(self.firebase_app)
            self.firebase_app = None
            
        # Get credentials
        cred = None
        if "service_account_json" in attributes:
            # Service account JSON is stored as a string
            service_account_json_str = attributes["service_account_json"].string_value
            service_account_json = json.loads(service_account_json_str)
            cred = credentials.Certificate(service_account_json)
        elif "service_account_file" in attributes:
            service_account_file = attributes["service_account_file"].string_value
            cred = credentials.Certificate(service_account_file)
            
        # Initialize Firebase app
        self.firebase_app = firebase_admin.initialize_app(cred)
        
        # Get FCM tokens
        fcm_tokens_value = attributes.get("fcm_tokens")
        if fcm_tokens_value:
            try:
                self.fcm_tokens = json.loads(fcm_tokens_value.string_value)
                if not isinstance(self.fcm_tokens, list):
                    self.fcm_tokens = [self.fcm_tokens]
            except json.JSONDecodeError:
                self.fcm_tokens = [fcm_tokens_value.string_value]
        
        # Get preset messages
        preset_messages_value = attributes.get("preset_messages")
        if preset_messages_value:
            self.preset_messages = json.loads(preset_messages_value.string_value)
        else:
            self.preset_messages = {}
            
        # Check if we should enforce using preset messages
        enforce_preset_value = attributes.get("enforce_preset")
        if enforce_preset_value:
            self.enforce_preset = enforce_preset_value.bool_value
        else:
            self.enforce_preset = False
            
        # Log successful configuration
        self.logger.info(f"Firebase Push configured with {len(self.fcm_tokens)} FCM tokens")
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
        
        # If preset is specified, use it
        if preset_name:
            if preset_name not in self.preset_messages:
                raise Exception(f"Preset '{preset_name}' not found in configured presets")
                
            preset = self.preset_messages[preset_name]
            title = preset.get("title", "")
            body = preset.get("body", "")
            
            # Apply template variables
            if template_vars:
                for key, value in template_vars.items():
                    placeholder = f"<<{key}>>"
                    title = title.replace(placeholder, str(value))
                    body = body.replace(placeholder, str(value))
        else:
            # Use direct parameters
            title = command.get("title", "")
            body = command.get("body", "")
            
        # Get data payload
        data = command.get("data", {})
        
        # Validate required fields
        if not title and not body:
            raise Exception("Either title or body must be provided")
            
        # Send notification to all configured FCM tokens
        failed_tokens = []
        successful_count = 0
        
        for token in self.fcm_tokens:
            try:
                message = messaging.Message(
                    notification=messaging.Notification(
                        title=title,
                        body=body,
                    ),
                    data=data,
                    token=token,
                )
                
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
        }
        
        return result

