"""Explicitly loaded experiment: export only loudness fields, never raw responses."""
from yt_dlp.extractor.youtube import YoutubeIE
from yt_dlp.extractor.niconico import NiconicoIE


class YoutubeLoudnessProbeIE(YoutubeIE, plugin_name='loudness_probe'):
    def _extract_player_responses(self, *args, **kwargs):
        responses, player_url = super()._extract_player_responses(*args, **kwargs)
        self._loudness_probe = []
        for response in responses:
            config = (response.get('playerConfig') or {}).get('audioConfig') or {}
            streams = response.get('streamingData') or {}
            self._loudness_probe.append({
                'audio_config': {k: v for k, v in config.items()
                                 if 'loudness' in k.lower()},
                'formats': [{k: f[k] for k in ('itag', 'loudnessDb', 'isDrc') if k in f}
                            for f in streams.get('adaptiveFormats', [])
                            if 'audio/' in f.get('mimeType', '')],
            })
        return responses, player_url

    def _real_extract(self, url):
        self._loudness_probe = []
        info = super()._real_extract(url)
        info['normalization_probe'] = self._loudness_probe
        return info


class NiconicoLoudnessProbeIE(NiconicoIE, plugin_name='loudness_probe'):
    def _extract_formats(self, api_data, video_id):
        fields = {}
        def walk(value, path=''):
            if isinstance(value, dict):
                for key, child in value.items():
                    child_path = f'{path}.{key}'
                    if 'loudness' in key.lower() or key == 'truePeak':
                        fields[child_path] = child
                    else:
                        walk(child, child_path)
            elif isinstance(value, list):
                for i, child in enumerate(value):
                    walk(child, f'{path}[{i}]')
        walk(api_data)
        self._loudness_probe = {'fields': fields, 'audios': [
            {'id': a.get('id'), 'keys': list(a.keys())}
            for a in api_data.get('media', {}).get('domand', {}).get('audios', [])]}
        return super()._extract_formats(api_data, video_id)

    def _real_extract(self, url):
        self._loudness_probe = []
        info = super()._real_extract(url)
        info['normalization_probe'] = self._loudness_probe
        return info
