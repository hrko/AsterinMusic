"""Experimental localized title override; explicitly loaded by the probe only."""
from yt_dlp.extractor.youtube import YoutubeIE, YoutubeSearchIE
from yt_dlp.utils import ExtractorError, traverse_obj


class _AsterinTitleIE(YoutubeIE, plugin_name='asterin_title'):
    def _download_initial_data(self, video_id, *args, **kwargs):
        data = super()._download_initial_data(video_id, *args, **kwargs)
        if self._preferred_lang:
            renderer = traverse_obj(data, (
                'contents', 'twoColumnWatchNextResults', 'results', 'results',
                'contents', ..., 'videoPrimaryInfoRenderer', {dict}), get_all=False)
            title = self._get_text(renderer, 'title') if renderer else None
            if isinstance(title, str) and title.strip():
                self._asterin_titles[video_id] = title
        return data

    def _real_extract(self, url):
        self._asterin_titles = {}
        try:
            info = super()._real_extract(url)
            title = self._asterin_titles.get(info.get('id'))
            if title and info.get('_type', 'video') == 'video':
                info['title'] = title
            # Opt-in experiment: search metadata can be localized even when
            # watch/next metadata is not. Never accept a different video ID.
            if (self._preferred_lang and info.get('_type', 'video') == 'video'
                    and self._configuration_arg('asterin_title_search', default=['false'])[0] == 'true'):
                try:
                    results = YoutubeSearchIE(self._downloader).extract('ytsearch3:' + info['id'])
                    for entry in results.get('entries', []):
                        if entry.get('id') != info['id']:
                            continue
                        candidate = entry.get('title')
                        if isinstance(candidate, str) and candidate.strip():
                            info['title'] = candidate
                        break
                except ExtractorError:
                    self.report_warning('Localized title search failed; keeping extracted title')
            return info
        finally:
            self._asterin_titles = {}
