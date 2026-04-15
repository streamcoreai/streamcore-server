import { StreamCoreAIPlugin } from '@streamcore/plugin';

const plugin = new StreamCoreAIPlugin();

plugin.onExecute(async (params: Record<string, unknown>) => {
  const location = params.location as string;
  if (!location) {
    return "No location was provided. Please ask the user which city or location they want the weather for.";
  }

  const encoded = encodeURIComponent(location);
  const url = `https://wttr.in/${encoded}?format=j1`;

  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Weather API returned status ${response.status}`);
  }

  const data = await response.json() as WeatherResponse;

  const current = data.current_condition?.[0];
  if (!current) {
    throw new Error(`No weather data found for "${location}"`);
  }

  const tempC = current.temp_C;
  const tempF = current.temp_F;
  const description = current.weatherDesc?.[0]?.value ?? "Unknown";
  const humidity = current.humidity;
  const windSpeedKmph = current.windspeedKmph;
  const feelsLikeC = current.FeelsLikeC;
  const feelsLikeF = current.FeelsLikeF;

  const area = data.nearest_area?.[0];
  const areaName = area?.areaName?.[0]?.value ?? location;
  const country = area?.country?.[0]?.value ?? "";

  const locationLabel = country ? `${areaName}, ${country}` : areaName;

  return `Current weather in ${locationLabel}: ${description}, ${tempC}°C (${tempF}°F), feels like ${feelsLikeC}°C (${feelsLikeF}°F), humidity ${humidity}%, wind ${windSpeedKmph} km/h.`;
});

plugin.run();

// Type definitions for the wttr.in JSON response
interface WeatherResponse {
  current_condition?: Array<{
    temp_C: string;
    temp_F: string;
    humidity: string;
    windspeedKmph: string;
    FeelsLikeC: string;
    FeelsLikeF: string;
    weatherDesc?: Array<{ value: string }>;
  }>;
  nearest_area?: Array<{
    areaName?: Array<{ value: string }>;
    country?: Array<{ value: string }>;
  }>;
}
